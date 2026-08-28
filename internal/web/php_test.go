package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/krmakmn/devbox/internal/fastcgi"
)

// fakePool, gelen CGI değişkenlerini geri yansıtan sahte bir PHP havuzudur.
type fakePool struct {
	lastParams map[string]string
	lastStdin  []byte
	status     int
	header     http.Header
	body       string
	err        error
}

func (f *fakePool) Do(ctx context.Context, params map[string]string, stdin io.Reader) (*fastcgi.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastParams = params
	if stdin != nil {
		f.lastStdin, _ = io.ReadAll(stdin)
	}

	status := f.status
	if status == 0 {
		status = 200
	}
	header := f.header
	if header == nil {
		header = http.Header{"Content-Type": {"text/html; charset=UTF-8"}}
	}
	body := f.body
	if body == "" {
		body = "merhaba"
	}
	return &fastcgi.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Stderr:     func() []byte { return nil },
	}, nil
}

// siteFixture, geçici bir belge kökü kurar.
func siteFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newHandler(root string, pool Requester) *Handler {
	return &Handler{
		Pool:         pool,
		DocumentRoot: root,
		ServerName:   "magaza.test",
		ServerPort:   "443",
		HTTPS:        true,
		SoftwareName: "DevBox/test",
	}
}

func TestServesFrontControllerForUnknownPath(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	pool := &fakePool{}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/urunler/42?renk=mavi", nil))

	if rec.Code != 200 {
		t.Fatalf("durum %d, gövde: %s", rec.Code, rec.Body)
	}
	if got, want := pool.lastParams["SCRIPT_NAME"], "/index.php"; got != want {
		t.Errorf("SCRIPT_NAME = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["SCRIPT_FILENAME"], filepath.Join(root, "index.php"); got != want {
		t.Errorf("SCRIPT_FILENAME = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["PATH_INFO"], "/urunler/42"; got != want {
		t.Errorf("PATH_INFO = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["QUERY_STRING"], "renk=mavi"; got != want {
		t.Errorf("QUERY_STRING = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["REQUEST_URI"], "/urunler/42?renk=mavi"; got != want {
		t.Errorf("REQUEST_URI = %q, beklenen %q", got, want)
	}
	if got := pool.lastParams["HTTPS"]; got != "on" {
		t.Errorf("HTTPS = %q, beklenen \"on\"", got)
	}
}

func TestServesStaticFileWithoutPHP(t *testing.T) {
	root := siteFixture(t, map[string]string{
		"index.php":       "<?php",
		"varlik/stil.css": "body{color:red}",
	})
	pool := &fakePool{}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/varlik/stil.css", nil))

	if rec.Code != 200 {
		t.Fatalf("durum %d", rec.Code)
	}
	if got := rec.Body.String(); got != "body{color:red}" {
		t.Errorf("gövde = %q", got)
	}
	if pool.lastParams != nil {
		t.Error("statik dosya için PHP çağrıldı")
	}
}

func TestRunsExplicitPHPScript(t *testing.T) {
	root := siteFixture(t, map[string]string{
		"index.php":         "<?php",
		"yonetim/panel.php": "<?php",
	})
	pool := &fakePool{}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/yonetim/panel.php/ayarlar", nil))

	if rec.Code != 200 {
		t.Fatalf("durum %d", rec.Code)
	}
	if got, want := pool.lastParams["SCRIPT_NAME"], "/yonetim/panel.php"; got != want {
		t.Errorf("SCRIPT_NAME = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["PATH_INFO"], "/ayarlar"; got != want {
		t.Errorf("PATH_INFO = %q, beklenen %q", got, want)
	}
}

// Klasik cgi.fix_pathinfo istismarı: saldırgan bir resim yükler, sonra
// /yuklemeler/kedi.jpg/x.php ister. php-cgi yolu geriye doğru yorumlayıp
// kedi.jpg'yi PHP olarak çalıştırabilir. Var olmayan bir betiği asla
// SCRIPT_FILENAME yapmadığımız için bu kapı kapalı olmalı.
func TestRejectsPathInfoCodeExecutionTrick(t *testing.T) {
	root := siteFixture(t, map[string]string{
		"index.php":           "<?php",
		"yuklemeler/kedi.jpg": "GIF89a<?php system($_GET['c']); ?>",
	})
	pool := &fakePool{}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/yuklemeler/kedi.jpg/x.php", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("durum %d, beklenen 404", rec.Code)
	}
	if pool.lastParams != nil {
		t.Fatalf("var olmayan betik PHP'ye gönderildi: %q", pool.lastParams["SCRIPT_FILENAME"])
	}
}

func TestBlocksHiddenFiles(t *testing.T) {
	root := siteFixture(t, map[string]string{
		"index.php":   "<?php",
		".env":        "DB_PASSWORD=gizli",
		".git/config": "[core]",
		".htaccess":   "deny from all",
	})
	h := newHandler(root, &fakePool{})

	for _, p := range []string{"/.env", "/.git/config", "/.htaccess"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: durum %d, beklenen 403", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "gizli") {
			t.Errorf("%s: dosya içeriği sızdı", p)
		}
	}
}

func TestAllowsWellKnownForACME(t *testing.T) {
	// ACME HTTP-01 doğrulaması bu dizini okuyabilmeli; yerel sertifika
	// üretimi buna bağlı.
	root := siteFixture(t, map[string]string{
		"index.php":                        "<?php",
		".well-known/acme-challenge/jeton": "kanıt",
	})
	h := newHandler(root, &fakePool{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/acme-challenge/jeton", nil))

	if rec.Code != 200 {
		t.Fatalf("durum %d, beklenen 200", rec.Code)
	}
	if got := rec.Body.String(); got != "kanıt" {
		t.Errorf("gövde = %q", got)
	}
}

func TestBlocksPathTraversal(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "sir.txt"), []byte("gizli"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "public")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newHandler(root, &fakePool{})
	for _, p := range []string{"/../sir.txt", "/a/../../sir.txt", "/%2e%2e/sir.txt"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if strings.Contains(rec.Body.String(), "gizli") {
			t.Errorf("%s: kök dışındaki dosya sunuldu", p)
		}
	}
}

// httpoxy (CVE-2016-5385): Proxy başlığı HTTP_PROXY'ye dönüşmemeli.
func TestDropsProxyHeader(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	pool := &fakePool{}
	h := newHandler(root, pool)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Proxy", "http://saldirgan.example:8080")
	req.Header.Set("X-Ozel", "değer")

	h.ServeHTTP(httptest.NewRecorder(), req)

	if got, ok := pool.lastParams["HTTP_PROXY"]; ok {
		t.Errorf("HTTP_PROXY aktarıldı: %q", got)
	}
	if got, want := pool.lastParams["HTTP_X_OZEL"], "değer"; got != want {
		t.Errorf("HTTP_X_OZEL = %q, beklenen %q", got, want)
	}
}

func TestForwardsRequestBodyAndContentHeaders(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	pool := &fakePool{}
	h := newHandler(root, pool)

	body := "ad=kerim&sehir=istanbul"
	req := httptest.NewRequest("POST", "/kaydet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := string(pool.lastStdin); got != body {
		t.Errorf("gövde = %q, beklenen %q", got, body)
	}
	if got, want := pool.lastParams["CONTENT_TYPE"], "application/x-www-form-urlencoded"; got != want {
		t.Errorf("CONTENT_TYPE = %q, beklenen %q", got, want)
	}
	if got, want := pool.lastParams["CONTENT_LENGTH"], fmt.Sprint(len(body)); got != want {
		t.Errorf("CONTENT_LENGTH = %q, beklenen %q", got, want)
	}
	// Bunlar HTTP_ önekiyle ikinci kez gitmemeli.
	for _, k := range []string{"HTTP_CONTENT_TYPE", "HTTP_CONTENT_LENGTH"} {
		if _, ok := pool.lastParams[k]; ok {
			t.Errorf("%s aktarılmamalıydı", k)
		}
	}
}

func TestPassesThroughStatusAndHeaders(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	pool := &fakePool{
		status: 404,
		header: http.Header{
			"Content-Type": {"application/json"},
			"Set-Cookie":   {"a=1", "b=2"},
		},
		body: `{"hata":"yok"}`,
	}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/urun/9", nil))

	if rec.Code != 404 {
		t.Errorf("durum %d, beklenen 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	cookies := rec.Header().Values("Set-Cookie")
	sort.Strings(cookies)
	if len(cookies) != 2 || cookies[0] != "a=1" || cookies[1] != "b=2" {
		t.Errorf("Set-Cookie = %v", cookies)
	}
	if got := rec.Body.String(); got != `{"hata":"yok"}` {
		t.Errorf("gövde = %q", got)
	}
}

func TestReturnsBadGatewayWhenPoolFails(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	h := newHandler(root, &fakePool{err: fmt.Errorf("havuz kapalı")})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("durum %d, beklenen 502", rec.Code)
	}
}

func TestDirectoryIndexIsServed(t *testing.T) {
	root := siteFixture(t, map[string]string{
		"index.php":      "<?php",
		"blog/index.php": "<?php",
	})
	pool := &fakePool{}
	h := newHandler(root, pool)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/blog/", nil))

	if got, want := pool.lastParams["SCRIPT_NAME"], "/blog/index.php"; got != want {
		t.Errorf("SCRIPT_NAME = %q, beklenen %q", got, want)
	}
}

func TestMissingPHPScriptIsNotFound(t *testing.T) {
	root := siteFixture(t, map[string]string{"index.php": "<?php"})
	pool := &fakePool{}
	h := newHandler(root, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/olmayan.php", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("durum %d, beklenen 404", rec.Code)
	}
	if pool.lastParams != nil {
		t.Error("var olmayan betik için PHP çağrıldı")
	}
}
