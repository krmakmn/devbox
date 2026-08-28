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
	"github.com/krmakmn/devbox/internal/dns"
	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/hostsfile"
	"github.com/krmakmn/devbox/internal/nrpt"
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
	case "dns":
		return runDNS(args[1:])
	case "edge":
		return runEdge(args[1:])
	case "runtime":
		return runRuntime(args[1:])
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
  devbox dns <alt komut>      *.test için yerel çözücü ve NRPT kuralı
  devbox edge [seçenekler]    80/443'ü dinle, host adına göre dağıt
  devbox runtime <alt komut>  PHP/Node gibi bileşenleri kur ve yönet
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

	exe, err := findPHPCGI(*phpPath, *phpVersion)
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
		rule := nrpt.Rule{
			Namespace: "." + suffixes[0],
			Servers:   []string{host},
			Comment:   nrpt.DefaultComment,
		}
		if err := nrpt.Add(rule); err != nil {
			return fmt.Errorf("%w\n\nYönetici olarak çalıştırın ya da geri düşüş için: devbox dns install -hosts -names <adlar>", err)
		}
		fmt.Printf("NRPT kuralı eklendi: %s → %s\n", rule.Namespace, host)
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
		if err := nrpt.Remove("." + suffixes[0]); err != nil {
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

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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
