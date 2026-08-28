package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenCreatesRootCA(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	root := s.RootCertificate()
	if !root.IsCA {
		t.Error("kök sertifika CA değil")
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("kök sertifikada CertSign yetkisi yok")
	}
	if !root.MaxPathLenZero {
		t.Error("ara CA üretimine izin veriliyor; zincir uzayabilir")
	}
	if got := root.NotAfter.Sub(root.NotBefore); got < 9*365*24*time.Hour {
		t.Errorf("kök ömrü %v, beklenen ~10 yıl", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca", "root.crt")); err != nil {
		t.Errorf("kök sertifika diske yazılmamış: %v", err)
	}

	// Kök anahtar başkasına okutulmamalı. Windows'ta izin modeli farklı
	// çalıştığı için orada denetlemiyoruz.
	if info, err := os.Stat(filepath.Join(dir, "ca", "root.key")); err == nil {
		if perm := info.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
			t.Errorf("kök anahtar izinleri çok geniş: %v", perm)
		}
	}
}

func TestOpenReusesExistingRoot(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !first.RootCertificate().Equal(second.RootCertificate()) {
		t.Error("ikinci açılışta yeni kök üretildi; güven deposundaki kök geçersizleşirdi")
	}
}

func TestCertificateHasWildcardSAN(t *testing.T) {
	s := openStore(t)
	cert, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	names := cert.Leaf.DNSNames
	want := map[string]bool{"magaza.test": false, "*.magaza.test": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("SAN listesinde %q yok: %v", n, names)
		}
	}
	if cert.Leaf.Subject.CommonName != "magaza.test" {
		t.Errorf("CN = %q", cert.Leaf.Subject.CommonName)
	}
}

func TestCertificateChainVerifies(t *testing.T) {
	s := openStore(t)
	cert, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}

	// Tarayıcının yapacağı doğrulamanın aynısı: zincir köke çıkıyor mu,
	// ad eşleşiyor mu, sunucu kimliği için mi kesilmiş.
	for _, host := range []string{"magaza.test", "admin.magaza.test"} {
		opts := x509.VerifyOptions{
			Roots:     s.RootPool(),
			DNSName:   host,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if _, err := cert.Leaf.Verify(opts); err != nil {
			t.Errorf("%s doğrulanamadı: %v", host, err)
		}
	}

	// İki seviye alt alan adı jokerin kapsamı dışında; sessizce geçmemeli.
	opts := x509.VerifyOptions{Roots: s.RootPool(), DNSName: "a.b.magaza.test"}
	if _, err := cert.Leaf.Verify(opts); err == nil {
		t.Error("iki seviye alt alan adı joker sertifikayla doğrulandı")
	}
}

func TestTLSHandshakeSucceedsWithRootInstalled(t *testing.T) {
	s := openStore(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls=%v host=%s", r.TLS != nil, r.Host)
	}))
	srv.TLS = s.TLSConfig()
	srv.StartTLS()
	defer srv.Close()

	// Köke güvenen istemci: uyarısız bağlanmalı. Tarayıcı deneyiminin
	// otomatikleştirilebilir karşılığı bu.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    s.RootPool(),
		ServerName: "magaza.test",
	}}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("güvenilen kökle el sıkışma başarısız: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "tls=true") {
		t.Errorf("gövde = %q", body)
	}

	// Köke güvenmeyen istemci: reddetmeli. Sertifikanın gerçekten kendi
	// CA'mıza bağlı olduğunun kanıtı.
	strict := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		ServerName: "magaza.test",
	}}}
	if _, err := strict.Get(srv.URL); err == nil {
		t.Error("köke güvenmeyen istemci bağlanabildi")
	}
}

func TestGetCertificateUsesSNIAndCollapsesSubdomains(t *testing.T) {
	s := openStore(t)

	cert, err := s.GetCertificate(&tls.ClientHelloInfo{ServerName: "admin.magaza.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	// Alt alan adı için ayrı sertifika kesilmemeli; joker zaten kapsıyor.
	if got := cert.Leaf.Subject.CommonName; got != "magaza.test" {
		t.Errorf("CN = %q, beklenen magaza.test", got)
	}

	// SNI göndermeyen istemci (IP ile bağlanan) hata almamalı.
	if _, err := s.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Errorf("SNI'sız el sıkışma hata verdi: %v", err)
	}
}

func TestCertificateIsCachedAndPersisted(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}

	// Yeniden açılan depo aynı sertifikayı diskten okumalı; her açılışta
	// yeni sertifika kesmek tarayıcı önbelleğini ve HSTS'i karıştırır.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Error("yeniden açılışta sertifika yeniden kesildi")
	}
}

func TestRenewsCertificateNearExpiry(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	s.now = func() time.Time { return base }
	first, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}

	// Yenileme eşiğinin hemen öncesi: aynı sertifika kalmalı.
	s.now = func() time.Time { return base.Add(leafTTL - renewBefore - time.Hour) }
	same, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.SerialNumber.Cmp(same.Leaf.SerialNumber) != 0 {
		t.Error("eşik dolmadan sertifika yenilendi")
	}

	// Eşiğin içinde: yenilenmeli.
	s.now = func() time.Time { return base.Add(leafTTL - renewBefore + time.Hour) }
	renewed, err := s.Certificate("magaza.test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) == 0 {
		t.Fatal("süresi dolmak üzere olan sertifika yenilenmedi")
	}
	if !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("yenilenen sertifikanın bitişi ileri alınmamış")
	}
}

func TestReplacesExpiredRoot(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := s.RootCertificate()

	// 11 yıl sonra açılan depo, süresi dolmuş kökle çalışmaya devam
	// etmemeli.
	s2 := &Store{dir: dir, cache: map[string]*tls.Certificate{},
		now: func() time.Time { return time.Now().Add(11 * 365 * 24 * time.Hour) }}
	if err := s2.loadOrCreateRoot(); err != nil {
		t.Fatal(err)
	}
	if s2.RootCertificate().Equal(old) {
		t.Error("süresi dolmuş kök yeniden kullanıldı")
	}
}

func TestRejectsInvalidNames(t *testing.T) {
	s := openStore(t)
	for _, name := range []string{"", "magaza..test", "a/b", strings.Repeat("x", 300)} {
		if _, err := s.Certificate(name); err == nil {
			t.Errorf("%q için hata beklenirken sertifika kesildi", name)
		}
	}
}

func TestBaseDomain(t *testing.T) {
	cases := map[string]string{
		"magaza.test":       "magaza.test",
		"admin.magaza.test": "magaza.test",
		"a.b.magaza.test":   "magaza.test",
		"localhost":         "localhost",
		"MAGAZA.TEST":       "magaza.test",
		"magaza.test.":      "magaza.test",
	}
	for in, want := range cases {
		if got := baseDomain(in); got != want {
			t.Errorf("baseDomain(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func TestWildcardNameIsNotDoubleWildcarded(t *testing.T) {
	s := openStore(t)
	cert, err := s.Certificate("*.magaza.test")
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	for _, n := range cert.Leaf.DNSNames {
		if strings.HasPrefix(n, "*.*") {
			t.Errorf("geçersiz joker üretildi: %q", n)
		}
	}
}
