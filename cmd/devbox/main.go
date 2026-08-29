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
	"unicode"

	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/dns"
	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/hostsfile"
	"github.com/krmakmn/devbox/internal/nrpt"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/phpini"
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
	case "dns":
		return runDNS(args[1:])
	case "edge":
		return runEdge(args[1:])
	case "runtime":
		return runRuntime(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	case "ps":
		return runPS(args[1:])
	case "logs":
		return runLogs(args[1:])
	case "privileged":
		return runPrivileged(args[1:])
	case "init":
		return runInit(args[1:])
	case "up":
		return runUp(args[1:])
	case "down":
		return runDown(args[1:])
	case "db":
		return runDB(args[1:])
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
  devbox init                 projeyi tanı ve devbox.yaml üret
  devbox up                   devbox.yaml'ı okuyup projeyi ayağa kaldır
  devbox down                 up'ın bıraktığı sunucu yapılandırmasını kaldır
  devbox serve [seçenekler]   bir dizini PHP ile sun
  devbox trust <alt komut>    yerel kök sertifikayı güven depolarına kur
  devbox dns <alt komut>      *.test için yerel çözücü ve NRPT kuralı
  devbox edge [seçenekler]    80/443'ü dinle, host adına göre dağıt
  devbox db <alt komut>       veritabanı örnekleri: kur, başlat, anlık görüntü
  devbox runtime <alt komut>  PHP/Node gibi bileşenleri kur ve yönet
  devbox daemon [seçenekler]  servisleri yöneten çekirdek süreci çalıştır
  devbox ps                   servislerin durumunu göster
  devbox logs [-f] <servis>   servisin günlüğünü göster
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
		phpVersion  = fs.String("php-version", "", "kullanılacak PHP sürümü (ör. 8.3); DevBox'ın kurduğu runtime'lardan seçilir")
		iniDir      = fs.String("ini", "", "hazır bir php.ini dizini kullan (verilmezse DevBox üretir)")
		xdebug      = fs.Bool("xdebug", false, "Xdebug'ı aç")
		phpExts     routeList
		phpSettings routeList
		serverName  = fs.String("server-name", "", "SERVER_NAME değeri (boşsa istekteki Host)")
		front       = fs.String("front-controller", "index.php", "ön denetleyici betiği")
		useTLS      = fs.Bool("tls", false, "HTTPS sun (yerel CA'dan sertifika üretilir)")
		domain      = fs.String("domain", "", "site alan adı; -tls ile sertifika bu ada kesilir")
		verbose     = fs.Bool("verbose", false, "ayrıntılı günlük")
	)
	fs.Var(&phpExts, "php-ext", "yüklenecek PHP uzantısı (birden çok kez verilebilir)")
	fs.Var(&phpSettings, "php-set", "php.ini ayarı, ad=değer (birden çok kez verilebilir)")
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

	exe, err := findPHPCGI(*phpPath, *phpVersion)
	if err != nil {
		return err
	}

	// php.ini: kullanıcı hazır bir dizin vermediyse projeye özel bir tane
	// üretiyoruz. Proje başına ayrı ini, bir projede Xdebug açıkken
	// diğerinin varsayılan ayarlarla çalışabilmesi demek.
	confDir := *iniDir
	if confDir == "" {
		generated, err := generatePHPIni(exe, filepath.Base(absRoot), *xdebug, phpExts, phpSettings)
		if err != nil {
			return err
		}
		confDir = generated
		logger.Info("php.ini üretildi", "dizin", confDir, "xdebug", *xdebug)
	}

	phpArgs := []string{"-c", confDir}

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
	// Alt komutu bayraklardan önce ayırıyoruz: flag paketi ilk konumsal
	// argümandan sonra ayrıştırmayı durdurur, yani "serve -addr ..."
	// yazıldığında -addr sessizce yok sayılırdı.
	sub, rest := splitSubcommand(args, "status")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("alt komut bayraklardan önce gelmeli: %q", fs.Arg(0))
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
		// Windows kök sertifika eklerken onay penceresi gösterir. Kullanıcı
		// bunu beklemezse pencereyi kaçırır ve komut asılı kalmış sanır.
		if runtime.GOOS == "windows" {
			fmt.Println("Windows bir onay penceresi gösterecek; kök sertifikayı eklemek için onaylayın.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		results, err := trust.Install(ctx, rootPath)
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

// routeList, tekrarlanabilir -route bayrağını toplar.
type routeList []string

func (r *routeList) String() string { return strings.Join(*r, ", ") }

func (r *routeList) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("yönlendirme host=hedef biçiminde olmalı: %q", v)
	}
	*r = append(*r, v)
	return nil
}

func runEdge(args []string) error {
	fs := flag.NewFlagSet("edge", flag.ContinueOnError)
	var routes routeList
	fs.Var(&routes, "route", "host=hedef (birden çok kez verilebilir), ör. magaza.test=http://127.0.0.1:8080")
	var (
		httpAddr  = fs.String("http", ":80", "HTTP dinleme adresi (HTTPS'e yönlendirir)")
		httpsAddr = fs.String("https", ":443", "HTTPS dinleme adresi")
		noTLS     = fs.Bool("no-tls", false, "yalnız HTTP sun (sertifika kullanma)")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox edge -route <host>=<hedef> [...]

80 ve 443'ü tek başına dinler, istekleri host adına göre arka uçlara dağıtır.
Böylece Apache, Nginx ve uygulama süreçleri aynı anda çalışabilir.

Örnek:
  devbox edge -route blog.test=http://127.0.0.1:8080 \
              -route api.test=http://127.0.0.1:8081

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(routes) == 0 {
		fs.Usage()
		return errors.New("en az bir -route gerekli")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e := edge.New()
	e.Logger = logger
	if _, port := splitPort(*httpsAddr); port != "" {
		e.HTTPSPort = port
	}

	for _, spec := range routes {
		host, target, _ := strings.Cut(spec, "=")
		host, target = strings.TrimSpace(host), strings.TrimSpace(target)
		if err := e.Proxy(host, target); err != nil {
			return err
		}
		logger.Info("yönlendirme", "host", host, "hedef", target)
	}

	srv := &edge.Server{Edge: e, HTTPAddr: *httpAddr, HTTPSAddr: *httpsAddr}
	if !*noTLS {
		store, err := certs.Open(paths.CertsDir())
		if err != nil {
			return fmt.Errorf("sertifika deposu açılamadı: %w", err)
		}
		srv.TLSConfig = store.TLSConfig()

		if installed, err := trust.IsInstalled(store.RootCertPath()); err == nil && !installed {
			logger.Warn("kök sertifika güven deposunda değil; tarayıcı uyarı verecek",
				"çözüm", "devbox trust install")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	logger.Info("kenar sunucu başladı", "http", *httpAddr, "https", *httpsAddr,
		"siteler", strings.Join(e.Hosts(), ", "))

	if *noTLS {
		httpSrv := &http.Server{Addr: *httpAddr, Handler: e, ReadHeaderTimeout: 15 * time.Second}
		go func() { <-ctx.Done(); httpSrv.Close() }()
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	return srv.ListenAndServe(ctx)
}

func runDNS(args []string) error {
	fs := flag.NewFlagSet("dns", flag.ContinueOnError)
	var (
		addr    = fs.String("addr", dns.DefaultAddr, "çözücünün dinleyeceği adres")
		suffix  = fs.String("suffix", "test", "sahiplenilecek son ek (virgülle birden fazla)")
		useHost = fs.Bool("hosts", false, "NRPT yerine hosts dosyasını kullan")
		names   = fs.String("names", "", "hosts kipinde yazılacak adlar (virgülle)")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox dns <alt komut> [seçenekler]

  serve      yerel çözücüyü çalıştır (Ctrl+C ile durur)
  install    NRPT kuralını ekle (yönetici hakkı ister)
  uninstall  NRPT kuralını kaldır
  status     kuralların durumunu göster

`)
		fs.PrintDefaults()
	}
	// Alt komutu bayraklardan önce ayırıyoruz: flag paketi ilk konumsal
	// argümandan sonra ayrıştırmayı durdurur, yani "serve -addr ..."
	// yazıldığında -addr sessizce yok sayılırdı.
	sub, rest := splitSubcommand(args, "status")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("alt komut bayraklardan önce gelmeli: %q", fs.Arg(0))
	}
	suffixes := splitList(*suffix)
	if len(suffixes) == 0 {
		return errors.New("en az bir son ek gerekli")
	}

	switch sub {
	case "serve":
		return serveDNS(*addr, suffixes)

	case "install":
		if *useHost || !nrpt.Supported() {
			return installHosts(splitList(*names))
		}
		host, _ := splitPort(*addr)
		namespace := "." + suffixes[0]
		rule := nrpt.Rule{Namespace: namespace, Servers: []string{host}, Comment: nrpt.DefaultComment}
		if err := rule.Validate(); err != nil {
			return err
		}

		err := elevateFor(
			[]string{"dns-install", "-namespace", namespace, "-server", host},
			func() error { return nrpt.Add(rule) },
		)
		if err != nil {
			return fmt.Errorf("%w\n\nGeri düşüş: devbox dns install -hosts -names <adlar>", err)
		}
		fmt.Printf("NRPT kuralı eklendi: %s → %s\n", namespace, host)
		fmt.Println("Çözücüyü çalıştırmayı unutmayın: devbox dns serve")
		return nil

	case "uninstall":
		if *useHost || !nrpt.Supported() {
			if err := hostsfile.Remove(hostsfile.Path()); err != nil {
				return err
			}
			fmt.Println("hosts dosyasındaki DevBox bloğu kaldırıldı")
			return nil
		}
		namespace := "." + suffixes[0]
		err := elevateFor(
			[]string{"dns-uninstall", "-namespace", namespace},
			func() error { return nrpt.Remove(namespace) },
		)
		if err != nil {
			return err
		}
		fmt.Println("NRPT kuralı kaldırıldı")
		return nil

	case "status":
		return dnsStatus(suffixes)

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

func serveDNS(addr string, suffixes []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv := dns.New(dns.Config{Addr: addr, Suffixes: suffixes, Logger: logger})

	if err := srv.Start(); err != nil {
		return fmt.Errorf("%w\n\nPort 53 başkası tarafından tutuluyor olabilir; -addr ile başka bir loopback adresi deneyin", err)
	}
	defer srv.Close()

	logger.Info("çözücü başladı", "adres", srv.Addr(), "son ekler", strings.Join(suffixes, ", "))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}

func installHosts(names []string) error {
	if len(names) == 0 {
		return errors.New("hosts kipinde -names ile en az bir alan adı verilmeli")
	}
	entries := []hostsfile.Entry{{IP: "127.0.0.1", Names: names}}
	path := hostsfile.Path()
	if err := hostsfile.Apply(path, entries); err != nil {
		return err
	}
	fmt.Printf("%s dosyasına yazıldı: %s\n", path, strings.Join(names, " "))
	fmt.Println("not: hosts joker desteklemez; alt alan adları için ayrı satır gerekir")
	return nil
}

func dnsStatus(suffixes []string) error {
	fmt.Println("son ekler:", strings.Join(suffixes, ", "))

	if nrpt.Supported() {
		rules, err := nrpt.List()
		if err != nil {
			fmt.Println("NRPT     : sorgulanamadı —", err)
		} else {
			var mine int
			for _, r := range rules {
				if r.Comment == nrpt.DefaultComment {
					mine++
					fmt.Printf("NRPT     : %s → %s\n", r.Namespace, strings.Join(r.Servers, ", "))
				}
			}
			if mine == 0 {
				fmt.Println("NRPT     : DevBox kuralı yok (devbox dns install)")
			}
		}
	} else {
		fmt.Println("NRPT     : bu platformda yok")
	}

	entries, err := hostsfile.Managed(hostsfile.Path())
	if err != nil {
		fmt.Println("hosts    : okunamadı —", err)
		return nil
	}
	if len(entries) == 0 {
		fmt.Println("hosts    : DevBox bloğu yok")
		return nil
	}
	for _, e := range entries {
		fmt.Printf("hosts    : %s %s\n", e.IP, strings.Join(e.Names, " "))
	}
	return nil
}

// splitSubcommand, alt komutu argümanların başından ayırır.
//
// Alt komut ilk sırada olmak zorunda; git, go ve docker de böyle çalışır.
// Bayrakların arasından alt komut aramaya kalkmak, "-addr 127.0.0.1 serve"
// gibi bir çağrıda bayrağın değerini alt komut sanmaya yol açar — hangi
// bayrağın değer aldığını bilmeden bunu doğru yapmanın yolu yok.
//
// Bayrakların ardından gelen bir alt komut sessizce yok sayılmaz: çağıran
// fs.NArg() ile artık argümanı görüp açık bir hata verir.
func splitSubcommand(args []string, fallback string) (sub string, rest []string) {
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return fallback, args
}

// splitNameAndFlags, alt komuttan sonraki konumsal adı bayraklardan ayırır.
//
// flag paketi ilk konumsal argümanda durduğu için "db create magaza -engine
// postgres" çağrısında -engine hiç ayrıştırılmıyordu. Adı önden çekince
// geri kalanı düz bayrak listesi oluyor ve iki sıra da çalışıyor:
// "create magaza -engine pg" ve "create -engine pg magaza".
func splitNameAndFlags(args []string) (name string, rest []string) {
	return splitSubcommand(args, "")
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// generatePHPIni, projeye özel php.ini üretir ve dizinini döner.
func generatePHPIni(phpExe, project string, xdebug bool, exts, settings []string) (string, error) {
	cfg := phpini.Config{Settings: map[string]string{}}

	// Runtime'ın kendi php.ini-development'ı varsa temel alınıyor: uzantı
	// dizini ve derlemeye özgü ayarlar oradan geliyor.
	phpDir := filepath.Dir(phpExe)
	for _, name := range []string{"php.ini-development", "php.ini-production"} {
		candidate := filepath.Join(phpDir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			cfg.BaseFile = candidate
			break
		}
	}
	if extDir := filepath.Join(phpDir, "ext"); dirExists(extDir) {
		cfg.ExtensionDir = extDir
	}

	for _, ext := range exts {
		cfg.Extensions = append(cfg.Extensions, strings.TrimSpace(ext))
	}
	for _, spec := range settings {
		key, value, found := strings.Cut(spec, "=")
		if !found {
			return "", fmt.Errorf("php ayarı ad=değer biçiminde olmalı: %q", spec)
		}
		cfg.Settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	if xdebug {
		x := &phpini.Xdebug{}
		// Uzantı dosyası bulunabiliyorsa yolunu veriyoruz; yoksa yalnız
		// ayarlar yazılıyor (uzantı temel dosyada yüklüyse yeter).
		for _, name := range []string{"php_xdebug.dll", "xdebug.so"} {
			candidate := filepath.Join(phpDir, "ext", name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				x.Extension = candidate
				break
			}
		}
		cfg.Xdebug = x
	}

	dir := filepath.Join(paths.DataDir(), "php", sanitizeName(project))
	if _, err := phpini.Write(dir, cfg); err != nil {
		return "", err
	}
	return dir, nil
}

// sanitizeName, proje adını dizin adı olarak güvenli hâle getirir.
//
// Unicode harflere izin veriyoruz: yalnız ASCII kabul etmek "mağaza"yı
// "ma-aza"ya çeviriyordu ve hedef kitle Türkçe proje adı kullanıyor.
// Elenen şey yol ayraçları, sürücü harfi iki noktası ve dosya sisteminin
// kabul etmediği karakterler.
func sanitizeName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	if sb.Len() == 0 {
		return "proje"
	}
	return sb.String()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// findPHPCGI, php-cgi'yi bulur.
//
// Sıra: açıkça verilen yol → DevBox'ın kurduğu runtime → PATH. Kendi
// kurduğumuzu PATH'ten önce denemek, makinede başka bir PHP olsa bile
// projenin beklediği sürümün kullanılmasını sağlıyor.
func findPHPCGI(given, versionSpec string) (string, error) {
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

	spec := "php"
	if versionSpec != "" {
		spec = "php@" + versionSpec
	}
	if inst, ok, err := runtimeStore().Resolve(spec); err == nil && ok {
		if bin, err := inst.Bin("php-cgi"); err == nil {
			if _, err := os.Stat(bin); err == nil {
				return bin, nil
			}
		}
	}
	if versionSpec != "" {
		return "", fmt.Errorf("php %s kurulu değil (devbox runtime install php@%s)", versionSpec, versionSpec)
	}

	found, err := exec.LookPath("php-cgi")
	if err != nil {
		return "", errors.New("php-cgi bulunamadı\n" +
			"  • DevBox ile kurun: devbox runtime install php@8.3\n" +
			"  • ya da yolunu verin: -php C:\\php\\php-cgi.exe")
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
