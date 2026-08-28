package phppool

import (
	"fmt"
	"net"
	"net/http"
	"net/http/fcgi"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Testler gerçek php-cgi'ye ihtiyaç duymasın diye test ikilisinin kendisi
// sahte bir php-cgi olarak yeniden çalıştırılıyor. FastCGI'yi standart
// kütüphanenin sunucu tarafı konuşuyor, yani tel üzerindeki protokol gerçek;
// taklit edilen tek şey PHP'nin kendisi.
//
// Davranışlar ortam değişkenleriyle ayarlanır:
//
//	FAKE_STARTUP_FAIL=1      hiç dinlemeden hata vererek çık
//	FAKE_STARTUP_DELAY=500ms  dinlemeye başlamadan önce bekle
//
// İstek başına davranış sorgu dizesiyle: ?sleep=200ms, ?crash=1
func TestMain(m *testing.M) {
	if os.Getenv("DEVBOX_FAKE_PHPCGI") == "1" {
		runFakePHPCGI()
		return
	}
	os.Exit(m.Run())
}

func runFakePHPCGI() {
	var addr string
	for i, a := range os.Args {
		if a == "-b" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}

	if os.Getenv("FAKE_STARTUP_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "sahte php-cgi: php.ini okunamadı")
		os.Exit(1)
	}
	if d := os.Getenv("FAKE_STARTUP_DELAY"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			time.Sleep(dur)
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sahte php-cgi: %s dinlenemedi: %v\n", addr, err)
		os.Exit(1)
	}

	var served atomic.Int64

	fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := served.Add(1)
		q := r.URL.Query()

		if q.Get("crash") == "1" {
			// Yanıt üretmeden ölüyoruz: php-cgi'nin ölümcül hatada
			// yaptığı şey.
			os.Exit(3)
		}
		if s := q.Get("sleep"); s != "" {
			if dur, err := time.ParseDuration(s); err == nil {
				time.Sleep(dur)
			}
		}
		fmt.Fprintf(w, "pid=%d istek=%d", os.Getpid(), n)
	}))
	os.Exit(0)
}
