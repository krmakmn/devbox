package edge

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/certs"
)

// backend, gelen isteği anlatan bir arka uç sunucusu döner.
func backend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "arka uç=%s host=%s yol=%s proto=%s ileten=%s upgrade=%s",
			name, r.Host, r.URL.Path,
			r.Header.Get("X-Forwarded-Proto"),
			r.Header.Get("X-Forwarded-For"),
			r.Header.Get("Upgrade"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, h http.Handler, host, path string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// Laragon'un "Apache mı Nginx mi" kısıtının kalktığının kanıtı: iki ayrı
// arka uç aynı anda, host adına göre.
func TestRoutesByHostToDifferentBackends(t *testing.T) {
	apache := backend(t, "apache")
	nginx := backend(t, "nginx")

	e := New()
	if err := e.Proxy("blog.test", apache.URL); err != nil {
		t.Fatal(err)
	}
	if err := e.Proxy("api.test", nginx.URL); err != nil {
		t.Fatal(err)
	}

	if _, body := get(t, e, "blog.test", "/yazi/1"); !strings.Contains(body, "arka uç=apache") {
		t.Errorf("blog.test yanlış arka uca gitti: %s", body)
	}
	if _, body := get(t, e, "api.test", "/v1/urun"); !strings.Contains(body, "arka uç=nginx") {
		t.Errorf("api.test yanlış arka uca gitti: %s", body)
	}
}

// Sanal sunucu eşleşmesi Host başlığına dayanır; hedefin adıyla
// değiştirirsek Apache ve Nginx yanlış siteyi servis eder.
func TestPreservesOriginalHostHeader(t *testing.T) {
	be := backend(t, "apache")
	e := New()
	if err := e.Proxy("magaza.test", be.URL); err != nil {
		t.Fatal(err)
	}

	_, body := get(t, e, "magaza.test", "/")
	if !strings.Contains(body, "host=magaza.test") {
		t.Errorf("özgün Host korunmadı: %s", body)
	}
}

func TestSetsForwardedHeaders(t *testing.T) {
	be := backend(t, "arka")
	e := New()
	if err := e.Proxy("magaza.test", be.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "magaza.test"
	req.RemoteAddr = "203.0.113.7:54321"
	// İstemcinin uydurduğu başlık arka uca sızmamalı.
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "203.0.113.7") {
		t.Errorf("gerçek istemci adresi iletilmedi: %s", body)
	}
	if strings.Contains(body, "1.2.3.4") {
		t.Errorf("istemcinin uydurduğu X-Forwarded-For arka uca geçti: %s", body)
	}
	if !strings.Contains(body, "proto=http ") {
		t.Errorf("X-Forwarded-Proto yanlış (TLS yok, http olmalı): %s", body)
	}
}

func TestWildcardAndExactPrecedence(t *testing.T) {
	genel := backend(t, "genel")
	ozel := backend(t, "ozel")

	e := New()
	if err := e.Proxy("*.magaza.test", genel.URL); err != nil {
		t.Fatal(err)
	}
	if err := e.Proxy("admin.magaza.test", ozel.URL); err != nil {
		t.Fatal(err)
	}

	// Tam eşleşme jokeri geçmeli.
	if _, body := get(t, e, "admin.magaza.test", "/"); !strings.Contains(body, "arka uç=ozel") {
		t.Errorf("tam eşleşme jokere yenildi: %s", body)
	}
	if _, body := get(t, e, "blog.magaza.test", "/"); !strings.Contains(body, "arka uç=genel") {
		t.Errorf("joker eşleşmedi: %s", body)
	}
	// Joker yalnız bir seviye kapsar.
	if resp, _ := get(t, e, "a.b.magaza.test", "/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("iki seviye alt alan adı jokere düştü: %d", resp.StatusCode)
	}
}

func TestHostMatchingIgnoresPortAndCase(t *testing.T) {
	be := backend(t, "arka")
	e := New()
	if err := e.Proxy("magaza.test", be.URL); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"magaza.test", "MAGAZA.TEST", "magaza.test:443", "magaza.test."} {
		resp, body := get(t, e, host, "/")
		if resp.StatusCode != 200 {
			t.Errorf("%q eşleşmedi (%d): %s", host, resp.StatusCode, body)
		}
	}
}

func TestUnknownHostListsKnownSites(t *testing.T) {
	be := backend(t, "arka")
	e := New()
	e.Proxy("magaza.test", be.URL)
	e.Proxy("*.blog.test", be.URL)

	resp, body := get(t, e, "yok.test", "/")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("durum %d, beklenen 404", resp.StatusCode)
	}
	// "Neden açılmıyor" sorusunun cevabı sayfada olsun.
	for _, want := range []string{"magaza.test", "*.blog.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("tanımlı site listelenmedi (%q):\n%s", want, body)
		}
	}
}

func TestDeadBackendGivesExplanatoryPage(t *testing.T) {
	e := New()
	// Kimsenin dinlemediği bir port.
	if err := e.Proxy("olu.test", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}

	resp, body := get(t, e, "olu.test", "/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("durum %d, beklenen 502", resp.StatusCode)
	}
	if !strings.Contains(body, "127.0.0.1:1") {
		t.Errorf("hata sayfası hedefi göstermiyor:\n%s", body)
	}
}

func TestHTTPRedirectsToHTTPS(t *testing.T) {
	e := New()
	h := e.RedirectHandler(nil)

	resp, _ := get(t, h, "magaza.test", "/urun/5?renk=mavi")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("durum %d, beklenen 301", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Location"), "https://magaza.test/urun/5?renk=mavi"; got != want {
		t.Errorf("Location = %q, beklenen %q", got, want)
	}
}

func TestHTTPRedirectKeepsNonStandardPort(t *testing.T) {
	e := New()
	e.HTTPSPort = "8443"
	resp, _ := get(t, e.RedirectHandler(nil), "magaza.test", "/")
	if got, want := resp.Header.Get("Location"), "https://magaza.test:8443/"; got != want {
		t.Errorf("Location = %q, beklenen %q", got, want)
	}
}

// ACME HTTP-01 sınaması düz HTTP ister; yönlendirirsek sertifika alınamaz.
func TestACMEChallengeIsNotRedirected(t *testing.T) {
	acme := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "kanıt")
	})
	e := New()

	resp, body := get(t, e.RedirectHandler(acme), "magaza.test", "/.well-known/acme-challenge/jeton")
	if resp.StatusCode != 200 {
		t.Fatalf("durum %d, beklenen 200 (yönlendirilmiş olabilir)", resp.StatusCode)
	}
	if body != "kanıt" {
		t.Errorf("gövde = %q", body)
	}
}

// Uçtan uca: gerçek TLS el sıkışması, kenar, ters vekil ve arka uç.
func TestEndToEndTLSTermination(t *testing.T) {
	be := backend(t, "apache")
	store, err := certs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	e := New()
	if err := e.Proxy("magaza.test", be.URL); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(e)
	srv.TLS = store.TLSConfig()
	srv.StartTLS()
	defer srv.Close()

	// İstemci magaza.test'e bağlanıyormuş gibi davransın ama bağlantıyı
	// test sunucusuna kursun.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: store.RootPool()},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(srv.URL, "https://"))
		},
	}}
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://magaza.test/panel")
	if err != nil {
		t.Fatalf("HTTPS isteği başarısız: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("durum %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "arka uç=apache") {
		t.Errorf("istek arka uca ulaşmadı: %s", body)
	}
	if !strings.Contains(string(body), "host=magaza.test") {
		t.Errorf("Host korunmadı: %s", body)
	}
	// TLS kenarda sonlandı; arka uç bunu X-Forwarded-Proto'dan bilmeli.
	if !strings.Contains(string(body), "proto=https") {
		t.Errorf("X-Forwarded-Proto https değil: %s", body)
	}
}

// WebSocket ve benzeri yükseltmeler kenardan geçebilmeli; Vite'ın sıcak
// yeniden yüklemesi buna bağlı.
func TestForwardsUpgradeRequests(t *testing.T) {
	upgraded := make(chan string, 1)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgraded <- r.Header.Get("Upgrade")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer be.Close()

	e := New()
	if err := e.Proxy("magaza.test", be.URL); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/ws", nil)
	req.Host = "magaza.test"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("yükseltme isteği: %v", err)
	}
	defer resp.Body.Close()

	select {
	case got := <-upgraded:
		if got != "websocket" {
			t.Errorf("arka uca giden Upgrade başlığı = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("yükseltme isteği arka uca ulaşmadı")
	}
}

func TestRemoveAndHosts(t *testing.T) {
	be := backend(t, "arka")
	e := New()
	e.Proxy("a.test", be.URL)
	e.Proxy("*.b.test", be.URL)

	hosts := e.Hosts()
	if len(hosts) != 2 || hosts[0] != "*.b.test" || hosts[1] != "a.test" {
		t.Errorf("Hosts() = %v", hosts)
	}

	e.Remove("a.test")
	e.Remove("*.b.test")
	if got := e.Hosts(); len(got) != 0 {
		t.Errorf("kaldırmadan sonra %v kaldı", got)
	}
}

func TestConcurrentRoutingIsSafe(t *testing.T) {
	be := backend(t, "arka")
	e := New()
	e.Proxy("magaza.test", be.URL)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%5 == 0 {
				// Yönlendirme tablosu istekler sürerken değişebilir.
				e.Proxy(fmt.Sprintf("site%d.test", i), be.URL)
				e.Remove(fmt.Sprintf("site%d.test", i))
				return
			}
			if resp, _ := get(t, e, "magaza.test", "/"); resp.StatusCode != 200 {
				t.Errorf("durum %d", resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
}

func TestProxyRejectsBadTargets(t *testing.T) {
	e := New()
	for _, target := range []string{"", "ftp://x", "://bozuk", "http://"} {
		if err := e.Proxy("a.test", target); err == nil {
			t.Errorf("geçersiz hedef kabul edildi: %q", target)
		}
	}
}
