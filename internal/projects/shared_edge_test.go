package projects

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/supervisor"
)

// TestIkiProjeAyniAndaCalisir, bu aracın var oluş sebebini kilitliyor.
//
// Paylaşılan kenardan önce her proje kendi kenarını 80/443'te açıyordu;
// ikinci proje portu alamadığı için aynı anda tek site çalışabiliyordu.
// Laragon'un yerine geçmek isteyen bir araçta bu, en temel eksik.
func TestIkiProjeAyniAndaCalisir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte çalıştırılabilir kabuk betiği")
	}

	paylasilan := edge.New()
	runner, sup := kurulumIkiProje(t, paylasilan)
	defer sup.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, ad := range []string{"bir", "iki"} {
		if _, err := runner.Start(ctx, ad); err != nil {
			t.Fatalf("%s başlatılamadı: %v", ad, err)
		}
	}

	// İkisi de kendi arka ucuna gitmeli. Aynı yanıtı almak, tek bir
	// projenin iki alan adını da yuttuğu anlamına gelirdi.
	for _, ad := range []string{"bir", "iki"} {
		govde, kod := iste(t, paylasilan, ad+".test", "")
		if kod != http.StatusOK {
			t.Fatalf("%s.test durumu %d", ad, kod)
		}
		if !strings.Contains(govde, "PROJE="+ad) {
			t.Errorf("%s.test yanlış projeye gitti: %q", ad, govde)
		}
	}

	// Biri durunca yalnız onun alan adı kalkmalı.
	if _, err := runner.Stop("iki"); err != nil {
		t.Fatal(err)
	}
	if _, kod := iste(t, paylasilan, "iki.test", ""); kod != http.StatusNotFound {
		t.Errorf("durdurulan projenin alan adı hâlâ kenarda: durum %d", kod)
	}
	if govde, kod := iste(t, paylasilan, "bir.test", ""); kod != http.StatusOK ||
		!strings.Contains(govde, "PROJE=bir") {
		t.Errorf("çalışan proje etkilendi: durum %d, gövde %q", kod, govde)
	}
}

// TestPostaKutusuKenardaKisitlaniyor, mimari değişiklikle kaybolabilecek
// bir güvenlik özelliğini koruyor.
//
// Paylaşılan kenar araya girince proje sürecinin gördüğü uzak adres her
// zaman 127.0.0.1 oluyor; yani projenin kendi loopback denetimi ağdan
// gelen isteği de geçirir. Kısıtı gerçek istemciyi gören kenarın
// uygulaması gerekiyor.
func TestPostaKutusuKenardaKisitlaniyor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte çalıştırılabilir kabuk betiği")
	}

	paylasilan := edge.New()
	runner, sup := kurulumIkiProje(t, paylasilan)
	defer sup.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := runner.Start(ctx, "bir"); err != nil {
		t.Fatal(err)
	}

	// Site ağdan açılmalı: kullanıcı telefonundan deniyor.
	if _, kod := iste(t, paylasilan, "bir.test", "192.0.2.10:5000"); kod != http.StatusOK {
		t.Errorf("site ağdan açılmıyor: durum %d", kod)
	}
	// Posta kutusu açılmamalı.
	if _, kod := iste(t, paylasilan, "mail.bir.test", "192.0.2.10:5000"); kod != http.StatusForbidden {
		t.Errorf("posta kutusu ağa açık: durum %d, 403 bekleniyordu", kod)
	}
	// Makinenin kendisinden açılmalı.
	if _, kod := iste(t, paylasilan, "mail.bir.test", "127.0.0.1:5000"); kod != http.StatusOK {
		t.Errorf("posta kutusu makinenin kendisinden açılmıyor: durum %d", kod)
	}
}

// iste, paylaşılan kenara verilen host ve uzak adresle bir istek yapar.
// uzak boşsa loopback varsayılır.
func iste(t *testing.T, e *edge.Edge, host, uzak string) (string, int) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
	r.Host = host
	if uzak != "" {
		r.RemoteAddr = uzak
	} else {
		r.RemoteAddr = "127.0.0.1:5000"
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w.Body.String(), w.Code
}

// kurulumIkiProje, sözleşmeye uyan iki sahte proje kurar.
//
// Sahte çalıştırılabilir gerçek "devbox up" gibi davranıyor: bir
// loopback sunucusu açıyor, bildirim satırını yazıyor, sonra hazır
// satırını. Sıra önemli — kenar bildirimi hazır olur olmaz arıyor.
func kurulumIkiProje(t *testing.T, paylasilan *edge.Edge) (*Runner, *supervisor.Supervisor) {
	t.Helper()
	root := t.TempDir()

	r := kayit(t)
	for _, ad := range []string{"bir", "iki"} {
		dir := proje(t, filepath.Join(root, ad), ad, ad+".test")
		if _, err := r.Add(dir); err != nil {
			t.Fatal(err)
		}
	}

	fake := filepath.Join(root, "sahte-devbox")
	betik := `#!/bin/sh
# -dir'den proje adını çıkar
dir=""
while [ $# -gt 0 ]; do
  case "$1" in
    -dir) dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
ad=$(basename "$dir")
` + sahteSunucuBetigi() + `
`
	if err := os.WriteFile(fake, []byte(betik), 0o755); err != nil {
		t.Fatal(err)
	}

	sup, err := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		Registry:   r,
		Supervisor: sup,
		Executable: fake,
		Edge:       paylasilan,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return runner, sup
}

// sahteSunucuBetigi, projenin iç sunucusunu taklit eden kabuk parçası.
//
// Go ile yazılmış bir yardımcıyı derlemek yerine nc kullanmak kırılgan
// olurdu; bunun yerine testin kendi ikili dosyasını yeniden çağırıyoruz.
func sahteSunucuBetigi() string {
	return fmt.Sprintf(`exec %q -test.run=TestSahteProjeSunucusu -- "$ad"`, os.Args[0])
}

// TestSahteProjeSunucusu, sahte çalıştırılabilir tarafından çağrılınca
// gerçek "devbox up"ın sözleşmesini yerine getirir. Doğrudan
// çalıştırıldığında hiçbir şey yapmaz.
func TestSahteProjeSunucusu(t *testing.T) {
	ad := ""
	for i, a := range os.Args {
		if a == "--" && i+1 < len(os.Args) {
			ad = os.Args[i+1]
		}
	}
	if ad == "" {
		t.Skip("yalnız sahte çalıştırılabilirden çağrılır")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "PROJE=%s host=%s", ad, r.Host)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	line, err := FormatEndpoint(Endpoint{
		Addr:      strings.TrimPrefix(srv.URL, "http://"),
		Hosts:     []string{ad + ".test"},
		LocalOnly: []string{"mail." + ad + ".test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(line)
	fmt.Printf("  %s%s%s.test\n", ad, ReadyLine, ad)

	time.Sleep(60 * time.Second)
}
