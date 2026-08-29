package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/dns"
	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/phppool"
	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/project"
	"github.com/krmakmn/devbox/internal/supervisor"
	"github.com/krmakmn/devbox/internal/trust"
	"github.com/krmakmn/devbox/internal/web"
	"github.com/krmakmn/devbox/internal/webserver"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "proje dizini")
	force := fs.Bool("force", false, "var olan devbox.yaml'ın üzerine yaz")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(absDir, project.FileName)); err == nil && !*force {
		return fmt.Errorf("%s zaten var (-force ile üzerine yazabilirsiniz)",
			filepath.Join(absDir, project.FileName))
	}

	detected := project.Detect(absDir)
	if err := detected.Config.Save(absDir); err != nil {
		return err
	}

	fmt.Printf("Algılanan: %s\n", detected.Framework)
	fmt.Printf("Yazıldı  : %s\n\n", filepath.Join(absDir, project.FileName))
	fmt.Printf("  ad        : %s\n", detected.Config.Name)
	fmt.Printf("  alan adı  : %s\n", detected.Config.Domain)
	fmt.Printf("  sunucu    : %s\n", detected.Config.Server)
	if detected.Config.Root != "" {
		fmt.Printf("  belge kökü: %s\n", detected.Config.Root)
	}
	if detected.Config.Proxy != "" {
		fmt.Printf("  hedef     : %s\n", detected.Config.Proxy)
	}
	for _, note := range detected.Notes {
		fmt.Printf("\n  • %s\n", note)
	}
	fmt.Println("\nBaşlatmak için: devbox up")
	return nil
}

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var (
		dir       = fs.String("dir", ".", "proje dizini")
		httpAddr  = fs.String("http", ":80", "HTTP dinleme adresi")
		httpsAddr = fs.String("https", ":443", "HTTPS dinleme adresi")
		noDNS     = fs.Bool("no-dns", false, "yerel çözücüyü çalıştırma")
		dnsAddr   = fs.String("dns", dns.DefaultAddr, "çözücünün dinleyeceği adres")
		phpPath   = fs.String("php", "", "php-cgi yolu (boşsa DevBox'ın kurduğu runtime ya da PATH)")
		serverBin = fs.String("server-bin", "", "apache/nginx çalıştırılabiliri; verilirse DevBox onu da yönetir")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox up [seçenekler]

devbox.yaml'ı okur ve projeyi ayağa kaldırır: sertifika, alan adı çözümlemesi,
PHP havuzu, web sunucusu yapılandırması ve kenar proxy. Ctrl+C ile durur.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	cfg, err := project.Load(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s bulunamadı; önce \"devbox init\" çalıştırın", project.FileName)
		}
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := &upSession{cfg: cfg, logger: logger, alloc: ports.New("127.0.0.1")}
	defer up.Close()

	if err := up.alloc.LoadExclusions(ctx); err != nil {
		// Rezervasyon listesi okunamazsa bağlama denemesi yine doğruyu
		// söylüyor; devam ediyoruz.
		logger.Debug("port rezervasyonları okunamadı", "hata", err)
	}

	handler, err := up.buildSite(ctx, *phpPath, *serverBin)
	if err != nil {
		return err
	}

	store, err := certs.Open(paths.CertsDir())
	if err != nil {
		return fmt.Errorf("sertifika deposu açılamadı: %w", err)
	}
	if _, err := store.Certificate(cfg.Domain); err != nil {
		return err
	}
	if installed, err := trust.IsInstalled(store.RootCertPath()); err == nil && !installed {
		logger.Warn("kök sertifika güven deposunda değil; tarayıcı uyarı verecek",
			"çözüm", "devbox trust install")
	}

	e := edge.New()
	e.Logger = logger
	if _, port := splitPort(*httpsAddr); port != "" {
		e.HTTPSPort = port
	}
	for _, host := range append([]string{cfg.Domain}, cfg.Aliases...) {
		e.Handle(host, handler)
	}

	if err := up.startProcesses(ctx); err != nil {
		return err
	}

	if !*noDNS {
		if err := up.startDNS(*dnsAddr, cfg.Domain); err != nil {
			// Çözücü açılamazsa proje yine çalışır; kullanıcı hosts
			// dosyasıyla ya da doğrudan adresle erişebilir.
			logger.Warn("yerel çözücü başlatılamadı; alan adı çözümlemesi elle ayarlanmalı",
				"hata", err)
		}
	}

	srv := &edge.Server{Edge: e, HTTPAddr: *httpAddr, HTTPSAddr: *httpsAddr, TLSConfig: store.TLSConfig()}

	fmt.Printf("\n  %s hazır: https://%s\n\n", cfg.Name, cfg.Domain)
	up.printSummary()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Println("\nkapatılıyor…")
		cancel()
	}()

	return srv.ListenAndServe(ctx)
}

// upSession, "devbox up" sırasında açılan kaynakları tutar.
type upSession struct {
	cfg    *project.Config
	logger *slog.Logger
	alloc  *ports.Allocator

	pool       *phppool.Pool
	sup        *supervisor.Supervisor
	dnsServer  *dns.Server
	backendURL string
	confPath   string
}

func (u *upSession) Close() {
	if u.sup != nil {
		u.sup.Close()
	}
	if u.pool != nil {
		u.pool.Close()
	}
	if u.dnsServer != nil {
		u.dnsServer.Close()
	}
}

// buildSite, yapılandırmadaki sunucu tipine göre isteği karşılayacak
// işleyiciyi kurar.
func (u *upSession) buildSite(ctx context.Context, phpPath, serverBin string) (http.Handler, error) {
	cfg := u.cfg

	if cfg.Server == project.ServerProxy {
		handler, err := edge.ProxyHandler(cfg.Domain, cfg.Proxy, u.logger)
		if err != nil {
			return nil, err
		}
		u.backendURL = cfg.Proxy
		return handler, nil
	}

	if err := u.startPHP(ctx, phpPath); err != nil {
		return nil, err
	}

	if cfg.Server == project.ServerDevBox {
		return &web.Handler{
			Pool:            u.pool,
			DocumentRoot:    cfg.DocumentRoot(),
			FrontController: cfg.FrontController,
			ServerName:      cfg.Domain,
			ServerPort:      "443",
			HTTPS:           true,
			SoftwareName:    "DevBox/" + version,
			Logger:          u.logger,
		}, nil
	}

	// apache ya da nginx: yapılandırmayı üret, sunucuyu (verilmişse)
	// başlat, kenarı ona yönlendir.
	return u.startWebServer(ctx, serverBin)
}

func (u *upSession) startPHP(ctx context.Context, phpPath string) error {
	cfg := u.cfg

	exe, err := findPHPCGI(phpPath, cfg.PHP.Version)
	if err != nil {
		return err
	}

	iniDir, err := generatePHPIniFromProject(exe, cfg)
	if err != nil {
		return err
	}

	basePort, err := u.alloc.Allocate(9000)
	if err != nil {
		return err
	}
	u.alloc.Release(basePort) // havuz kendi tahsisini yapacak

	pool, err := phppool.New(phppool.Config{
		Name:        cfg.Name,
		Exec:        exe,
		Args:        []string{"-c", iniDir},
		WorkDir:     cfg.DocumentRoot(),
		Workers:     cfg.PHP.Workers,
		BasePort:    basePort,
		MaxRequests: 500,
		Env:         envList(cfg.Env),
		Logger:      u.logger,
	})
	if err != nil {
		return err
	}
	u.pool = pool

	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pool.Ready(readyCtx); err != nil {
		return fmt.Errorf("php-cgi ayağa kalkmadı (%s): %w", exe, err)
	}
	return nil
}

func (u *upSession) startWebServer(ctx context.Context, serverBin string) (http.Handler, error) {
	cfg := u.cfg

	listenPort, err := u.alloc.Allocate(8080)
	if err != nil {
		return nil, err
	}
	listen := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))

	logDir := filepath.Join(paths.DataDir(), "log", sanitizeName(cfg.Name))
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}

	site := webserver.Site{
		Name:            sanitizeName(cfg.Name),
		Domain:          cfg.Domain,
		Aliases:         cfg.Aliases,
		DocumentRoot:    cfg.DocumentRoot(),
		Listen:          listen,
		PHPBackends:     u.pool.Addrs(),
		FrontController: cfg.FrontController,
		LogDir:          logDir,
	}

	var driver webserver.Driver
	switch cfg.Server {
	case project.ServerApache:
		driver = &webserver.Apache{Binary: serverBin}
	case project.ServerNginx:
		driver = &webserver.Nginx{Binary: serverBin}
	default:
		return nil, fmt.Errorf("bilinmeyen sunucu %q", cfg.Server)
	}

	confPath := filepath.Join(paths.DataDir(), "conf", cfg.Server, sanitizeName(cfg.Name)+".conf")
	if err := webserver.Apply(ctx, driver, confPath, []webserver.Site{site}); err != nil {
		return nil, err
	}
	u.confPath = confPath
	u.backendURL = "http://" + listen

	return edge.ProxyHandler(cfg.Domain, u.backendURL, u.logger)
}

func (u *upSession) startProcesses(ctx context.Context) error {
	if len(u.cfg.Processes) == 0 {
		return nil
	}
	sup, err := supervisor.New(u.logger)
	if err != nil {
		return err
	}
	u.sup = sup

	for name, command := range u.cfg.Processes {
		parts, err := splitArgs(command)
		if err != nil || len(parts) == 0 {
			return fmt.Errorf("%s süreci çözümlenemedi: %q", name, command)
		}
		svc, err := sup.Add(supervisor.Config{
			Name:    name,
			Exec:    parts[0],
			Args:    parts[1:],
			WorkDir: u.cfg.Dir(),
			Env:     envList(u.cfg.Env),
		})
		if err != nil {
			return err
		}
		startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = svc.Start(startCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s süreci başlatılamadı: %w", name, err)
		}
	}
	return nil
}

func (u *upSession) startDNS(addr, domain string) error {
	// Alan adının son ekini sahipleniyoruz: magaza.test için "test".
	suffix := domain
	if i := strings.LastIndex(domain, "."); i >= 0 {
		suffix = domain[i+1:]
	}

	srv := dns.New(dns.Config{Addr: addr, Suffixes: []string{suffix}, Logger: u.logger})
	if err := srv.Start(); err != nil {
		return err
	}
	u.dnsServer = srv
	return nil
}

func (u *upSession) printSummary() {
	fmt.Printf("  sunucu    : %s\n", u.cfg.Server)
	if u.backendURL != "" {
		fmt.Printf("  arka uç   : %s\n", u.backendURL)
	}
	if u.pool != nil {
		fmt.Printf("  php       : %d işçi, %s\n", len(u.pool.Addrs()), strings.Join(u.pool.Addrs(), ", "))
	}
	if u.confPath != "" {
		fmt.Printf("  yapılandırma: %s\n", u.confPath)
	}
	if u.dnsServer != nil {
		fmt.Printf("  çözücü    : %s\n", u.dnsServer.Addr())
	}
	if u.sup != nil {
		for _, s := range u.sup.Status() {
			fmt.Printf("  süreç     : %s (%s)\n", s.Name, s.State)
		}
	}
	fmt.Println("\n  Ctrl+C ile durdurun.")
}

// envList, harita biçimindeki ortam değişkenlerini "K=V" dizisine çevirir.
func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		if strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// generatePHPIniFromProject, devbox.yaml'daki PHP ayarlarından php.ini
// üretir ve dizinini döner.
func generatePHPIniFromProject(phpExe string, cfg *project.Config) (string, error) {
	settings := make([]string, 0, len(cfg.PHP.Ini))
	for k, v := range cfg.PHP.Ini {
		settings = append(settings, k+"="+v)
	}
	return generatePHPIni(phpExe, cfg.Name, cfg.PHP.Xdebug, cfg.PHP.Extensions, settings)
}
