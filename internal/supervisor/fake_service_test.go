package supervisor

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Testler gerçek mysqld ya da httpd istemesin diye test ikilisi sahte bir
// servis olarak yeniden çalıştırılıyor. Davranış ortam değişkenleriyle
// ayarlanıyor:
//
//	FAKE_LISTEN=127.0.0.1:0   bu adresi dinle (0 verilirse gerçek portu yaz)
//	FAKE_PORT_FILE=<yol>      dinlenen adresi bu dosyaya yaz
//	FAKE_STARTUP_DELAY=500ms  dinlemeye başlamadan önce bekle
//	FAKE_LOG=<metin>          başlarken bu metni stdout'a yaz
//	FAKE_EXIT_CODE=1          dinlemeden bu kodla çık
//	FAKE_LIVE_FOR=2s          bu süre sonra kendiliğinden çık
func runFakeService() {
	if delay := os.Getenv("FAKE_STARTUP_DELAY"); delay != "" {
		if d, err := time.ParseDuration(delay); err == nil {
			time.Sleep(d)
		}
	}
	if code := os.Getenv("FAKE_EXIT_CODE"); code != "" {
		n, _ := strconv.Atoi(code)
		fmt.Fprintln(os.Stderr, "sahte servis: başlatma hatası, çıkılıyor")
		os.Exit(n)
	}

	var ln net.Listener
	if addr := os.Getenv("FAKE_LISTEN"); addr != "" {
		var err error
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sahte servis:", err)
			os.Exit(1)
		}
		if file := os.Getenv("FAKE_PORT_FILE"); file != "" {
			os.WriteFile(file, []byte(ln.Addr().String()), 0o644)
		}
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "pid=%d", os.Getpid())
		}))
	}

	// Günlük satırı dinlemeye başladıktan sonra yazılıyor: LogReady'nin
	// gerçekten hazır olmayı beklediğini sınayabilmek için.
	if msg := os.Getenv("FAKE_LOG"); msg != "" {
		fmt.Println(msg)
	}

	if live := os.Getenv("FAKE_LIVE_FOR"); live != "" {
		if d, err := time.ParseDuration(live); err == nil {
			time.Sleep(d)
			os.Exit(0)
		}
	}
	select {}
}
