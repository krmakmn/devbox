package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/phppool"
)

// Bu dosya katmanları birlikte sınar: HTTP sunucusu → Handler → süreç havuzu
// → ayrı bir işletim sistemi süreci → FastCGI → geri. Aradaki tek taklit,
// PHP yorumlayıcısının kendisi; protokol ve süreç yönetimi gerçek.
func TestMain(m *testing.M) {
	if os.Getenv("DEVBOX_WEB_FAKE_PHPCGI") == "1" {
		runFakeInterpreter()
		return
	}
	os.Exit(m.Run())
}

func runFakeInterpreter() {
	var addr string
	for i, a := range os.Args {
		if a == "-b" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := fcgi.ProcessEnv(r)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		fmt.Fprintf(w, "script=%s\n", env["SCRIPT_FILENAME"])
		fmt.Fprintf(w, "kok=%s\n", env["DOCUMENT_ROOT"])
		fmt.Fprintf(w, "yontem=%s\n", r.Method)
		fmt.Fprintf(w, "uri=%s\n", r.URL.RequestURI())
		fmt.Fprintf(w, "govde=%s\n", body)
	}))
	os.Exit(0)
}

func newLiveSite(t *testing.T, files map[string]string) (*httptest.Server, string) {
	t.Helper()
	root := siteFixture(t, files)

	pool, err := phppool.New(phppool.Config{
		Name:         "e2e",
		Exec:         os.Args[0],
		Env:          []string{"DEVBOX_WEB_FAKE_PHPCGI=1"},
		Workers:      2,
		SpawnTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("havuz kurulamadı: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pool.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	srv := httptest.NewServer(&Handler{
		Pool:         pool,
		DocumentRoot: root,
		ServerName:   "magaza.test",
		ServerPort:   "80",
		SoftwareName: "DevBox/e2e",
	})
	t.Cleanup(srv.Close)
	return srv, root
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("gövde okunamadı: %v", err)
	}
	return resp, string(body)
}

func TestEndToEndFrontController(t *testing.T) {
	srv, root := newLiveSite(t, map[string]string{"index.php": "<?php"})

	resp, body := get(t, srv.URL+"/urunler/42?renk=mavi")
	if resp.StatusCode != 200 {
		t.Fatalf("durum %d, gövde: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "script="+root) || !strings.Contains(body, "index.php") {
		t.Errorf("SCRIPT_FILENAME beklenen betiği göstermiyor:\n%s", body)
	}
	if !strings.Contains(body, "uri=/urunler/42?renk=mavi") {
		t.Errorf("REQUEST_URI aktarılmamış:\n%s", body)
	}
}

func TestEndToEndPostBody(t *testing.T) {
	srv, _ := newLiveSite(t, map[string]string{"index.php": "<?php"})

	form := strings.NewReader("ad=kerim&sehir=istanbul")
	resp, err := http.Post(srv.URL+"/kaydet", "application/x-www-form-urlencoded", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "govde=ad=kerim&sehir=istanbul") {
		t.Errorf("istek gövdesi PHP'ye ulaşmamış:\n%s", body)
	}
	if !strings.Contains(string(body), "yontem=POST") {
		t.Errorf("REQUEST_METHOD yanlış:\n%s", body)
	}
}

func TestEndToEndStaticAndBlockedPaths(t *testing.T) {
	srv, _ := newLiveSite(t, map[string]string{
		"index.php": "<?php",
		"stil.css":  "body{margin:0}",
		".env":      "DB_PASSWORD=gizli",
	})

	if resp, body := get(t, srv.URL+"/stil.css"); resp.StatusCode != 200 || body != "body{margin:0}" {
		t.Errorf("statik dosya sunulamadı: %d %q", resp.StatusCode, body)
	}
	resp, body := get(t, srv.URL+"/.env")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf(".env durumu %d, beklenen 403", resp.StatusCode)
	}
	if strings.Contains(body, "gizli") {
		t.Error(".env içeriği sızdı")
	}
}

func TestEndToEndConcurrentRequests(t *testing.T) {
	srv, _ := newLiveSite(t, map[string]string{"index.php": "<?php"})

	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		go func(i int) {
			resp, err := http.Get(fmt.Sprintf("%s/sayfa/%d", srv.URL, i))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("durum %d", resp.StatusCode)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < 30; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("eşzamanlı istek başarısız: %v", err)
		}
	}
}
