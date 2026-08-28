// devbox, Windows için yerel geliştirme ortamının komut satırı aracıdır.
//
// Şu an tek bir işi yapıyor: bir dizini php-cgi süreç havuzu üzerinden HTTP'de
// sunmak. Yol haritasındaki kenar proxy, alan adı, TLS ve veritabanı katmanları
// bunun üzerine gelecek.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/krmakmn/devbox/internal/phppool"
	"github.com/krmakmn/devbox/internal/web"
)

// version, sürüm etiketi. Yayınlarda -ldflags ile doldurulur.
var version = "0.1.0-gelistirme"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "devbox:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("komut belirtilmedi")
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("devbox %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("bilinmeyen komut %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `devbox — Windows yerel geliştirme ortamı

Kullanım:
  devbox serve [seçenekler]   bir dizini PHP ile sun
  devbox version              sürümü yazdır
  devbox help                 bu yardımı göster

"devbox serve -h" ile sunucu seçeneklerini görebilirsiniz.
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Kullanım: devbox serve [seçenekler]")
		fs.PrintDefaults()
	}

	var (
		root        = fs.String("root", ".", "belge kökü (Laravel'de public/)")
		phpPath     = fs.String("php", "", "php-cgi çalıştırılabilirinin yolu (boşsa PATH'te aranır)")
		addr        = fs.String("addr", "127.0.0.1:8080", "dinlenecek adres")
		workers     = fs.Int("workers", 0, "php-cgi süreç sayısı (0 = CPU sayısı)")
		maxRequests = fs.Int("max-requests", 500, "bir süreç kaç istekten sonra yenilenir (0 = sınırsız)")
		iniDir      = fs.String("ini", "", "projeye özel php.ini dizini (php-cgi -c)")
		serverName  = fs.String("server-name", "", "SERVER_NAME değeri (boşsa istekteki Host)")
		front       = fs.String("front-controller", "index.php", "ön denetleyici betiği")
		verbose     = fs.Bool("verbose", false, "ayrıntılı günlük")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("belge kökü çözülemedi: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("belge kökü bir dizin değil: %s", absRoot)
	}

	exe, err := findPHPCGI(*phpPath)
	if err != nil {
		return err
	}

	var phpArgs []string
	if *iniDir != "" {
		phpArgs = append(phpArgs, "-c", *iniDir)
	}

	pool, err := phppool.New(phppool.Config{
		Name:        filepath.Base(absRoot),
		Exec:        exe,
		Args:        phpArgs,
		WorkDir:     absRoot,
		Workers:     *workers,
		MaxRequests: *maxRequests,
		Logger:      logger,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStart()
	if err := pool.Ready(startCtx); err != nil {
		return fmt.Errorf("php-cgi ayağa kalkmadı (%s): %w", exe, err)
	}

	_, port := splitPort(*addr)
	handler := &web.Handler{
		Pool:            pool,
		DocumentRoot:    absRoot,
		FrontController: *front,
		ServerName:      *serverName,
		ServerPort:      port,
		SoftwareName:    "DevBox/" + version,
		Logger:          logger,
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	logger.Info("sunucu başladı",
		"adres", "http://"+*addr,
		"kök", absRoot,
		"php", exe,
		"işçi", pool.Stats().Workers,
	)

	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errc:
		return err
	case <-stop:
		logger.Info("kapatılıyor", "sayaçlar", fmt.Sprintf("%+v", pool.Stats()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("sunucu zarifçe kapanmadı", "hata", err)
	}
	return pool.Close()
}

// findPHPCGI, php-cgi'yi bulur. Verilen yol doğrudan denenir; verilmediyse
// PATH'te aranır (Windows'ta .exe uzantısı otomatik eklenir).
func findPHPCGI(given string) (string, error) {
	if given != "" {
		abs, err := filepath.Abs(given)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("php-cgi bulunamadı: %s", abs)
		}
		return abs, nil
	}

	found, err := exec.LookPath("php-cgi")
	if err != nil {
		return "", errors.New("php-cgi PATH'te yok; -php ile yolunu verin " +
			"(Windows'ta genellikle C:\\php\\php-cgi.exe)")
	}
	return found, nil
}

func splitPort(addr string) (host, port string) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, ""
	}
	return addr[:i], addr[i+1:]
}
