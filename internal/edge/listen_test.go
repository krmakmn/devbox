package edge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestListenPortTutulmussaHataVerir, gerçek Windows'ta çıkan kusuru
// kilitliyor: "devbox up" projeyi hazır ilan edip sonra dinlemeye
// çalışıyordu. Port tutulamadığında kullanıcı "hazır: https://…"
// satırını görüyor, ardından anlamsız bir soket hatası alıyordu.
//
// Listen'in Serve'den ayrılmasının tek sebebi bu; test de tam olarak
// bunu ölçüyor: bağlanma başarısızlığı hizmet başlamadan önce görülmeli.
func TestListenPortTutulmussaHataVerir(t *testing.T) {
	tutulan, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("yardımcı dinleyici açılamadı: %v", err)
	}
	defer tutulan.Close()

	srv := &Server{
		Edge:      New(),
		HTTPAddr:  tutulan.Addr().String(),
		HTTPSAddr: "127.0.0.1:0",
		TLSConfig: &tls.Config{},
	}

	err = srv.Listen()
	if err == nil {
		srv.Close()
		t.Fatal("tutulan portta Listen hata vermeliydi")
	}

	var be *BindError
	if !errors.As(err, &be) {
		t.Fatalf("hata BindError olmalı, %T geldi: %v", err, err)
	}
	if be.Addr != srv.HTTPAddr {
		t.Errorf("BindError.Addr = %q, %q bekleniyordu", be.Addr, srv.HTTPAddr)
	}
	if be.Port != portOf(srv.HTTPAddr) {
		t.Errorf("BindError.Port = %d, %d bekleniyordu", be.Port, portOf(srv.HTTPAddr))
	}
}

// TestListenIkinciBasarisizOlursaBirinciSizmaz, hata yolunda kaynak
// bırakmadığımızı doğruluyor. HTTP açılıp HTTPS açılamazsa, açılmış olan
// kapatılmalı — yoksa kullanıcı hatayı düzeltip yeniden denediğinde bu
// kez kendi sızdırdığımız dinleyiciye çarpar.
func TestListenIkinciBasarisizOlursaBirinciSizmaz(t *testing.T) {
	tutulan, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("yardımcı dinleyici açılamadı: %v", err)
	}
	defer tutulan.Close()

	// Boş bir port bulup hemen bırakıyoruz; HTTP bunu alacak.
	gecici, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("geçici dinleyici açılamadı: %v", err)
	}
	bosAdres := gecici.Addr().String()
	gecici.Close()

	srv := &Server{
		Edge:      New(),
		HTTPAddr:  bosAdres,
		HTTPSAddr: tutulan.Addr().String(), // dolu: ikinci bağlanma patlar
		TLSConfig: &tls.Config{},
	}

	if err := srv.Listen(); err == nil {
		srv.Close()
		t.Fatal("ikinci port dolu iken Listen hata vermeliydi")
	}

	// Birinci dinleyici kapatılmışsa aynı adres yeniden bağlanabilir.
	yeniden, err := net.Listen("tcp", bosAdres)
	if err != nil {
		t.Fatalf("HTTP dinleyicisi sızmış: %s yeniden bağlanamadı: %v", bosAdres, err)
	}
	yeniden.Close()
}

// TestServeListenSonrasiCalisir, ayrımın işlevi bozmadığını gösteriyor:
// Listen ile açılan dinleyicilerde Serve gerçekten hizmet veriyor.
func TestServeListenSonrasiCalisir(t *testing.T) {
	arka := httptestSunucu(t, "MERHABA")

	e := New()
	e.Handle("ornek.test", arka)

	srv := &Server{
		Edge:      e,
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
		TLSConfig: gecerliTLS(t),
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	adres := srv.httpLn.Addr().String()

	ctx, iptal := context.WithCancel(context.Background())
	bitti := make(chan error, 1)
	go func() { bitti <- srv.Serve(ctx) }()

	// HTTP dinleyicisi yalnız HTTPS'e yönlendiriyor; yönlendirmenin
	// gelmesi dinleyicinin gerçekten ayakta olduğunu kanıtlar.
	istemci := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	istek, _ := http.NewRequest(http.MethodGet, "http://"+adres+"/", nil)
	istek.Host = "ornek.test"

	yanit, err := istemci.Do(istek)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer yanit.Body.Close()
	if yanit.StatusCode < 300 || yanit.StatusCode > 399 {
		t.Errorf("durum %d, yönlendirme bekleniyordu", yanit.StatusCode)
	}

	iptal()
	select {
	case <-bitti:
	case <-time.After(15 * time.Second):
		t.Fatal("Serve bağlam iptalinde dönmedi")
	}
}

// gecerliTLS, tek kullanımlık kendinden imzalı bir sertifika üretir.
// HTTPS dinleyicisi sertifikasız açılmıyor ve açılmazsa Serve her iki
// dinleyiciyi birden kapatıyor — testin ölçmek istediği şey bu değil.
func gecerliTLS(t *testing.T) *tls.Config {
	t.Helper()

	anahtar, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("anahtar üretilemedi: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ornek.test"},
		DNSNames:     []string{"ornek.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &anahtar.PublicKey, anahtar)
	if err != nil {
		t.Fatalf("sertifika üretilemedi: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: anahtar}},
	}
}

func httptestSunucu(t *testing.T, govde string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(govde))
	})
}
