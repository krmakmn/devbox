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

	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/phppool"
	"github.com/krmakmn/devbox/internal/trust"
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
	case "trust":
		return runTrust(args[1:])
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
  devbox trust <alt komut>    yerel kök sertifikayı güven depolarına kur
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
		useTLS      = fs.Bool("tls", false, "HTTPS sun (yerel CA'dan sertifika üretilir)")
		domain      = fs.String("domain", "", "site alan adı; -tls ile sertifika bu ada kesilir")
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

	scheme := "http"
	if *useTLS {
		store, err := certs.Open(paths.CertsDir())
		if err != nil {
			return fmt.Errorf("sertifika deposu açılamadı: %w", err)
		}
		name := *domain
		if name == "" {
			name = "localhost"
		}
		// Sertifikayı şimdi kesiyoruz ki bir sorun varsa ilk istekte değil
		// açılışta görülsün.
		if _, err := store.Certificate(name); err != nil {
			return err
		}
		srv.TLSConfig = store.TLSConfig()
		handler.HTTPS = true
		scheme = "https"

		if installed, err := trust.IsInstalled(store.RootCertPath()); err == nil && !installed {
			logger.Warn("kök sertifika güven deposunda değil; tarayıcı uyarı verecek",
				"çözüm", "devbox trust install")
		}
	}

	logger.Info("sunucu başladı",
		"adres", scheme+"://"+*addr,
		"kök", absRoot,
		"php", exe,
		"işçi", pool.Stats().Workers,
	)

	errc := make(chan error, 1)
	go func() {
		var err error
		if *useTLS {
			// Sertifika ve anahtar TLSConfig'ten geliyor.
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

func runTrust(args []string) error {
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox trust <alt komut>

  install     kök sertifikayı güven depolarına kur (Windows + Firefox)
  status      kurulu mu diye bak
  uninstall   işletim sistemi güven deposundan kaldır
  path        kök sertifika dosyasının yolunu yazdır
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	sub := "status"
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}

	store, err := certs.Open(paths.CertsDir())
	if err != nil {
		return fmt.Errorf("sertifika deposu açılamadı: %w", err)
	}
	rootPath := store.RootCertPath()

	switch sub {
	case "path":
		fmt.Println(rootPath)
		return nil

	case "status":
		installed, err := trust.IsInstalled(rootPath)
		fmt.Println("kök sertifika:", rootPath)
		fmt.Println("geçerlilik   :", store.RootCertificate().NotAfter.Format("2006-01-02"))
		switch {
		case err != nil:
			fmt.Println("güven deposu : sorgulanamadı —", err)
		case installed:
			fmt.Println("güven deposu : kurulu")
		default:
			fmt.Println("güven deposu : kurulu değil (devbox trust install)")
		}
		return nil

	case "install":
		results, err := trust.Install(rootPath)
		if err != nil {
			return err
		}
		var installed int
		for _, r := range results {
			fmt.Println(r)
			if r.Installed {
				installed++
			}
		}
		if installed == 0 {
			return errors.New("hiçbir güven deposuna kurulamadı")
		}
		fmt.Printf("\n%d hedefe kuruldu. Açık tarayıcıları yeniden başlatın.\n", installed)
		return nil

	case "uninstall":
		if err := trust.Uninstall(rootPath); err != nil {
			return err
		}
		fmt.Println("kök sertifika işletim sistemi güven deposundan kaldırıldı")
		fmt.Println("not: Firefox profillerinden kaldırmak için certutil -D -d sql:<profil> -n \"DevBox yerel geliştirme CA\"")
		return nil

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
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
