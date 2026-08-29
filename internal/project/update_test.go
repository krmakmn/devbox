package project

import (
	"strings"
	"testing"
)

const ornekYaml = `# Mağaza projesi — ekip notu: PHP 8.2'de kalıyoruz, 8.3 ile
# ödeme kütüphanesi patlıyor.
name: magaza
domain: magaza.test
php:
  version: "8.2" # yükseltmeden önce ödeme testlerini koş
  workers: 8
`

// TestUpdateYorumlariKoruyor, bu dosyanın var olma sebebini kilitliyor.
//
// Config.Save ile kaydetmek ölçüldü ve iki yorumu da siliyordu.
// devbox.yaml depoya ekleniyor; panelden yapılan tek bir kaydetme
// ekibin notlarını silip git'e gönderirdi.
func TestUpdateYorumlariKoruyor(t *testing.T) {
	out, err := Update([]byte(ornekYaml), map[string]any{"php.version": "8.3"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	metin := string(out)

	if !strings.Contains(metin, "ekip notu") {
		t.Errorf("üst yorum kaybolmuş:\n%s", metin)
	}
	if !strings.Contains(metin, "ödeme testlerini koş") {
		t.Errorf("satır yorumu kaybolmuş:\n%s", metin)
	}
	if !strings.Contains(metin, `version: "8.3"`) {
		t.Errorf("sürüm değişmemiş:\n%s", metin)
	}
	// Yalnız DEĞER değişmiş olmalı. "8.2" metnini tümden aramak yanlış
	// olurdu: üst yorumun kendisi "PHP 8.2'de kalıyoruz" diyor ve o
	// cümlenin korunması zaten istediğimiz şey.
	if strings.Contains(metin, `version: "8.2"`) {
		t.Errorf("eski sürüm değeri kalmış:\n%s", metin)
	}
	if !strings.Contains(metin, "PHP 8.2'de kalıyoruz") {
		t.Errorf("yorumdaki metin bozulmuş:\n%s", metin)
	}
}

// TestUpdateYazilmamisVarsayilaniEklemez, Save'in ikinci kusurunu
// kilitliyor: kullanıcının yazmadığı alanları dosyaya sabitlemek,
// "varsayılan neyse o" demenin anlamını değiştirir.
func TestUpdateYazilmamisVarsayilaniEklemez(t *testing.T) {
	out, err := Update([]byte(ornekYaml), map[string]any{"php.workers": 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, istenmeyen := range []string{"server:", "frontController:"} {
		if strings.Contains(string(out), istenmeyen) {
			t.Errorf("%q yazılmamıştı ama eklenmiş:\n%s", istenmeyen, out)
		}
	}
}

// TestUpdateSurumuTirnakliyor, somut bir tuzağı kapatıyor: "8.3"
// tırnaksız yazılırsa YAML onu ondalık sayı olarak okur.
func TestUpdateSurumuTirnakliyor(t *testing.T) {
	out, err := Update([]byte("name: a\ndomain: a.test\n"), map[string]any{"php.version": "8.3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `version: "8.3"`) {
		t.Errorf("sürüm tırnaklanmamış:\n%s", out)
	}
	// Ve gerçekten geri okunabilmeli.
	cfg, err := Parse(out)
	if err != nil {
		t.Fatalf("üretilen dosya okunamadı: %v", err)
	}
	if cfg.PHP.Version != "8.3" {
		t.Errorf("sürüm = %q", cfg.PHP.Version)
	}
}

func TestUpdateAlanEkleyipSilebiliyor(t *testing.T) {
	out, err := Update([]byte(ornekYaml), map[string]any{"server": "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "server: nginx") {
		t.Errorf("alan eklenmemiş:\n%s", out)
	}

	out, err = Update(out, map[string]any{"php.workers": nil})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "workers") {
		t.Errorf("alan silinmemiş:\n%s", out)
	}
	if !strings.Contains(string(out), "ekip notu") {
		t.Errorf("silme yorumları da götürmüş:\n%s", out)
	}
}

// TestUpdateGecersizDegeriDiskeYazdirmaz, panelden gelen hatalı bir
// değerin kullanıcıyı bozuk bir dosyayla baş başa bırakmamasını
// sağlıyor.
func TestUpdateGecersizDegeriReddeder(t *testing.T) {
	_, err := Update([]byte(ornekYaml), map[string]any{"server": "boyle-bir-sunucu-yok"})
	if err == nil {
		t.Fatal("geçersiz sunucu kabul edildi")
	}
	if !strings.Contains(err.Error(), "geçersiz") {
		t.Errorf("hata sebebi belirsiz: %v", err)
	}
}

func TestUpdateIcIceAlanOlusturur(t *testing.T) {
	out, err := Update([]byte("name: a\ndomain: a.test\n"), map[string]any{
		"php.extensions": []string{"redis", "gd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(out)
	if err != nil {
		t.Fatalf("üretilen dosya okunamadı: %v\n%s", err, out)
	}
	if strings.Join(cfg.PHP.Extensions, ",") != "redis,gd" {
		t.Errorf("uzantılar = %v", cfg.PHP.Extensions)
	}
}
