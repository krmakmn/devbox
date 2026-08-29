package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krmakmn/devbox/internal/projects"
	"github.com/krmakmn/devbox/internal/supervisor"
)

const yapilandirma = `# Ekip notu: PHP 8.2'de kalıyoruz, ödeme kütüphanesi 8.3'te patlıyor.
name: magaza
domain: magaza.test
php:
  version: "8.2" # yükseltmeden önce ödeme testlerini koş
  workers: 8
`

// ayarSunucusu, tek projeli bir API sunucusu kurar ve adres, jeton ile
// proje dizinini döner.
func ayarSunucusu(t *testing.T) (adres, jeton, dizin string) {
	t.Helper()

	dizin = t.TempDir()
	if err := os.WriteFile(filepath.Join(dizin, "devbox.yaml"), []byte(yapilandirma), 0o644); err != nil {
		t.Fatal(err)
	}

	kayitDosyasi := filepath.Join(t.TempDir(), "projeler.json")
	reg, err := projects.Open(kayitDosyasi)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(dizin); err != nil {
		t.Fatal(err)
	}

	sup, err := supervisor.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sup.Close() })

	jeton, err = GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{
		Token:      jeton,
		Supervisor: sup,
		Projects:   &projects.Runner{Registry: reg, Supervisor: sup},
	})
	if err != nil {
		t.Fatal(err)
	}
	adres, err = srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return adres, jeton, dizin
}

func istek(t *testing.T, yontem, url, jeton, govde string) (*http.Response, string) {
	t.Helper()
	var okuyucu *bytes.Reader
	if govde == "" {
		okuyucu = bytes.NewReader(nil)
	} else {
		okuyucu = bytes.NewReader([]byte(govde))
	}
	req, err := http.NewRequest(yontem, url, okuyucu)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+jeton)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestProjeYapilandirmasiOkunuyor(t *testing.T) {
	adres, jeton, _ := ayarSunucusu(t)

	resp, govde := istek(t, http.MethodGet, "http://"+adres+"/v1/projects/magaza/config", jeton, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("durum %d: %s", resp.StatusCode, govde)
	}

	var out ConfigResponse
	if err := json.Unmarshal([]byte(govde), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Raw, "Ekip notu") {
		t.Error("ham içerik dönmemiş")
	}
	if out.Config == nil || out.Config.PHP.Version != "8.2" {
		t.Errorf("yapılandırma çözülmemiş: %+v", out.Config)
	}
	if len(out.Options.Servers) == 0 {
		t.Error("sunucu seçenekleri boş")
	}
}

// TestProjeYapilandirmasiKaydedilirkenYorumlarKoruyor, panelin
// kullanıcının dosyasını bozmamasını uçtan uca doğruluyor.
func TestProjeYapilandirmasiKaydedilirkenYorumlarKoruyor(t *testing.T) {
	adres, jeton, dizin := ayarSunucusu(t)

	resp, govde := istek(t, http.MethodPut,
		"http://"+adres+"/v1/projects/magaza/config", jeton,
		`{"changes":{"php.version":"8.3","php.workers":4}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("durum %d: %s", resp.StatusCode, govde)
	}

	diskte, err := os.ReadFile(filepath.Join(dizin, "devbox.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	metin := string(diskte)

	if !strings.Contains(metin, "Ekip notu") {
		t.Errorf("üst yorum silinmiş:\n%s", metin)
	}
	if !strings.Contains(metin, "ödeme testlerini koş") {
		t.Errorf("satır yorumu silinmiş:\n%s", metin)
	}
	if !strings.Contains(metin, `version: "8.3"`) {
		t.Errorf("sürüm yazılmamış:\n%s", metin)
	}
	// JSON sayıyı float64 yapıyor; dosyaya "4.0" değil "4" yazılmalı,
	// yoksa yapılandırma çözülemez.
	if !strings.Contains(metin, "workers: 4\n") {
		t.Errorf("işçi sayısı tam sayı yazılmamış:\n%s", metin)
	}
}

func TestProjeYapilandirmasiGecersizDegeriReddediyor(t *testing.T) {
	adres, jeton, dizin := ayarSunucusu(t)
	once, _ := os.ReadFile(filepath.Join(dizin, "devbox.yaml"))

	resp, _ := istek(t, http.MethodPut,
		"http://"+adres+"/v1/projects/magaza/config", jeton,
		`{"changes":{"server":"boyle-bir-sey-yok"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("durum %d, 400 bekleniyordu", resp.StatusCode)
	}

	// En önemlisi: dosyaya dokunulmamış olmalı.
	sonra, _ := os.ReadFile(filepath.Join(dizin, "devbox.yaml"))
	if string(once) != string(sonra) {
		t.Errorf("geçersiz istek dosyayı değiştirmiş:\n%s", sonra)
	}
}

func TestProjeYapilandirmasiJetonsuzReddediyor(t *testing.T) {
	adres, _, _ := ayarSunucusu(t)

	resp, _ := istek(t, http.MethodGet, "http://"+adres+"/v1/projects/magaza/config", "yanlis-jeton", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("okuma durumu %d, 401 bekleniyordu", resp.StatusCode)
	}
	resp, _ = istek(t, http.MethodPut, "http://"+adres+"/v1/projects/magaza/config", "yanlis-jeton",
		`{"changes":{"php.version":"8.3"}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("yazma durumu %d, 401 bekleniyordu", resp.StatusCode)
	}
}

func TestProjeYapilandirmasiBilinmeyenProjeyiReddediyor(t *testing.T) {
	adres, jeton, _ := ayarSunucusu(t)
	resp, _ := istek(t, http.MethodGet, "http://"+adres+"/v1/projects/yok/config", jeton, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("durum %d, 404 bekleniyordu", resp.StatusCode)
	}
}

// TestYapilandirmaJSONAlanAdlariKucukHarf, gerçek tarayıcıda çıkan bir
// kusuru kilitliyor.
//
// project.Config'in JSON etiketi yoktu; API "php.version" değil
// "PHP.Version" gönderiyordu. Panel alanları boş yüklüyor, kaydetme o
// boşluğu diske yazıyor ve kullanıcının hiç dokunmadığı sürüm satırını
// — yorumuyla birlikte — siliyordu. Yani yorumları korumak için yazılan
// bütün makine başka bir kapıdan atlatılmıştı.
//
// Alan adlarının devbox.yaml ile aynı olması ayrıca panelin değişiklik
// yollarıyla ("php.version") okuma yollarını aynı tutuyor.
func TestYapilandirmaJSONAlanAdlariKucukHarf(t *testing.T) {
	adres, jeton, _ := ayarSunucusu(t)

	_, govde := istek(t, http.MethodGet, "http://"+adres+"/v1/projects/magaza/config", jeton, "")

	var ham map[string]any
	if err := json.Unmarshal([]byte(govde), &ham); err != nil {
		t.Fatal(err)
	}
	cfg, ok := ham["config"].(map[string]any)
	if !ok {
		t.Fatalf("config nesnesi yok: %s", govde)
	}
	if _, ok := cfg["name"]; !ok {
		t.Errorf("\"name\" alanı yok; Go alan adları sızmış olmalı: %v", anahtarlar(cfg))
	}
	php, ok := cfg["php"].(map[string]any)
	if !ok {
		t.Fatalf("\"php\" alanı yok: %v", anahtarlar(cfg))
	}
	if php["version"] != "8.2" {
		t.Errorf("php.version = %v, \"8.2\" bekleniyordu", php["version"])
	}
	if php["workers"] != float64(8) {
		t.Errorf("php.workers = %v, 8 bekleniyordu", php["workers"])
	}
}

func anahtarlar(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
