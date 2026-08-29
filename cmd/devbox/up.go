package main

import (
	"context"
	"crypto/tls"
	"errors"
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
	"github.com/krmakmn/devbox/internal/container"
	"github.com/krmakmn/devbox/internal/cron"
	"github.com/krmakmn/devbox/internal/dns"
	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/inspect"
	"github.com/krmakmn/devbox/internal/mail"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/phppool"
	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/project"
	"github.com/krmakmn/devbox/internal/projects"
	"github.com/krmakmn/devbox/internal/services"
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
		internal  = fs.String("internal", "", "80/443'ü açmak yerine işleyiciyi bu loopback adresinde düz HTTP ile sun (paylaşılan kenar için)")
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

	store, err := certs.Open(paths.CertsDir())
	if err != nil {
		return fmt.Errorf("sertifika deposu açılamadı: %w", err)
	}
	if _, err := store.Certificate(cfg.Domain); err != nil {
		return err
	}
	if installed, err := trust.IsInstalled(store.RootCertPath(), trust.ScopeUser); err == nil && !installed {
		logger.Warn("kök sertifika güven deposunda değil; tarayıcı uyarı verecek",
			"çözüm", "devbox trust install")
	}

	e := edge.New()
	e.Logger = logger
	if _, port := splitPort(*httpsAddr); port != "" {
		e.HTTPSPort = port
	}

	// Posta ve yan servisler siteden ÖNCE başlıyor. Sıralama bir
	// kusurun düzeltilmesi: PHP havuzu kurulurken projectEnv henüz boştu,
	// dolayısıyla PHP uygulaması ne posta yakalayıcının adresini ne de
	// Redis gibi servislerin değişkenlerini görüyordu. Laravel'in
	// env('MAIL_PORT') çağrısı boş dönüyordu ve posta hiç yakalanmıyordu.
	up.startMail(e)

	if err := up.startServices(ctx, e); err != nil {
		return err
	}

	handler, err := up.buildSite(ctx, *phpPath, *serverBin)
	if err != nil {
		return err
	}
	for _, host := range append([]string{cfg.Domain}, cfg.Aliases...) {
		e.Handle(host, handler)
	}

	up.startInspector(e, store, *httpsAddr)

	if err := up.startProcesses(ctx); err != nil {
		return err
	}

	if err := up.startCron(ctx); err != nil {
		return err
	}

	// Paylaşılan kenar kipinde çözücü çekirdek sürecin işi; her proje
	// kendi çözücüsünü açmaya kalkarsa ilki dışındakiler "adres kullanımda"
	// uyarısı verir ve günlük anlamsız gürültüyle dolar.
	if !*noDNS && *internal == "" {
		if err := up.startDNS(*dnsAddr, cfg.Domain); err != nil {
			// Çözücü açılamazsa proje yine çalışır; kullanıcı hosts
			// dosyasıyla ya da doğrudan adresle erişebilir.
			logger.Warn("yerel çözücü başlatılamadı; alan adı çözümlemesi elle ayarlanmalı",
				"hata", err)
		}
	}

	wrap := up.inspectorWrap()

	// Paylaşılan kenar kipi: 80/443'ü çekirdek süreç dinliyor, biz
	// yalnız kendi işleyicimizi loopback'te sunup adresi bildiriyoruz.
	if *internal != "" {
		return up.serveInternal(ctx, cancel, e, wrap, *internal)
	}

	srv := &edge.Server{Edge: e, HTTPAddr: *httpAddr, HTTPSAddr: *httpsAddr, TLSConfig: store.TLSConfig()}
	srv.Wrap = wrap

	// Dinleyiciler "hazır" demeden ÖNCE açılıyor. Gerçek Windows'ta
	// çıkan bir kusurdu: 80 rezerve bir aralığa düştüğünde kullanıcı
	// önce "hazır: https://…" satırını görüyor, sonra anlamsız bir
	// soket hatası alıyor ve tarayıcıya gidince hiçbir şey bulamıyordu.
	if err := srv.Listen(); err != nil {
		return bindHatasi(ctx, err)
	}

	// Bu satır aynı zamanda çekirdek süreçle sözleşme: projects.Runner
	// projeyi "hazır" saymak için tam olarak bunu arıyor. Metni tek bir
	// yerde tutuyoruz ki biri değişip diğeri kalmasın.
	fmt.Printf("\n  %s%s%s\n\n", cfg.Name, projects.ReadyLine, cfg.Domain)
	up.printSummary()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Println("\nkapatılıyor…")
		cancel()
	}()

	return srv.Serve(ctx)
}

// bindHatasi, uç dinleyicisi açılamadığında hatayı kullanıcının bir şey
// yapabileceği hâle getirir.
//
// Ham hata ("bind: An attempt was made to access a socket in a way
// forbidden by its access permissions") hiçbir şey anlatmıyor. Sebep
// çoğu zaman Windows'un Hyper-V/WSL2/Docker Desktop yüzünden rezerve
// ettiği port aralıkları ya da IIS; ikisi de teşhis edilebilir.
func bindHatasi(ctx context.Context, err error) error {
	var be *edge.BindError
	if !errors.As(err, &be) || be.Port == 0 {
		return err
	}

	alloc := ports.New("127.0.0.1")
	// Rezerve aralıklar okunamazsa açıklama yine veriliyor, yalnız
	// "rezerve mi" kısmı eksik kalıyor. Teşhis uğruna başarısız olmak
	// anlamsız.
	_ = alloc.LoadExclusions(ctx)

	return fmt.Errorf("%w\n\n%s", err, strings.TrimRight(alloc.Diagnose(be.Port), "\n"))
}

// runDown, "devbox up"ın makinede bıraktığı izleri temizler.
//
// Çalışan bir "devbox up"ı durdurmuyor — o ön planda çalışıyor ve Ctrl+C
// ile duruyor. Buradaki iş, kalıcı olarak yazılmış şeyleri geri almak:
// üretilen web sunucusu yapılandırması.
//
// NRPT kuralına ve hosts girdisine kasten dokunulmuyor: onlar tek bir
// projeye değil, .test son ekinin tamamına ait. Bir projeyi kapatırken
// diğerlerinin alan adı çözümlemesini bozmak kabul edilemez. Onlar için
// ayrı bir komut var: devbox dns uninstall.
func runDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	dir := fs.String("dir", ".", "proje dizini")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox down [seçenekler]

"devbox up"ın makinede bıraktığı web sunucusu yapılandırmasını kaldırır.
Çalışan bir sunucuyu durdurmaz; onu Ctrl+C ile kapatın.

Alan adı çözümlemesi (NRPT / hosts) tüm .test alan adlarına ait olduğu için
burada kaldırılmaz: devbox dns uninstall

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
			return fmt.Errorf("%s bulunamadı", project.FileName)
		}
		return err
	}

	if cfg.Server != project.ServerApache && cfg.Server != project.ServerNginx {
		fmt.Printf("%s için kaldırılacak yapılandırma yok (sunucu: %s)\n", cfg.Name, cfg.Server)
		return nil
	}

	confPath := filepath.Join(paths.DataDir(), "conf", cfg.Server, sanitizeName(cfg.Name)+".conf")
	if _, err := os.Stat(confPath); err != nil {
		fmt.Printf("%s için yapılandırma bulunamadı: %s\n", cfg.Name, confPath)
		return nil
	}
	if err := os.Remove(confPath); err != nil {
		return fmt.Errorf("yapılandırma kaldırılamadı: %w", err)
	}
	fmt.Printf("kaldırıldı: %s\n", confPath)
	fmt.Printf("%s sunucusunu yeniden yükleyin ki değişiklik etkili olsun.\n", cfg.Server)
	return nil
}

// upSession, "devbox up" sırasında açılan kaynakları tutar.
type upSession struct {
	cfg    *project.Config
	logger *slog.Logger
	alloc  *ports.Allocator

	pool       *phppool.Pool
	sup        *supervisor.Supervisor
	dnsServer  *dns.Server
	mailSMTP   *mail.SMTPServer
	cronRunner *cron.Runner
	relayTo    []string
	svcManager *services.Manager
	containers []containerRef
	inspector  *inspect.Recorder
	phpError   error
	backendURL string
	confPath   string

	// localHosts, yalnız makinenin kendisinden açılabilecek alan
	// adları. Paylaşılan kenar kipinde kısıtı uygulayacak taraf kenar
	// olduğu için ona bildirmemiz gerekiyor.
	localHosts []string
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
	if u.mailSMTP != nil {
		u.mailSMTP.Close()
	}
	if u.cronRunner != nil {
		u.cronRunner.Close()
	}
	// Konteynerler --rm ile açılıyor ve docker istemcisi durunca çoğu
	// zaman kendiliğinden gidiyorlar; garanti değil, bu yüzden ad
	// üzerinden temizliyoruz.
	for _, ref := range u.containers {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := container.Remove(ctx, ref.runtime, ref.spec.ContainerName()); err != nil {
			u.logger.Warn("konteyner temizlenemedi", "ad", ref.spec.ContainerName(), "hata", err)
		}
		cancel()
	}
}

// containerRef, kapanışta temizlenecek konteyner.
type containerRef struct {
	runtime string
	spec    container.Spec
	domain  string
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

	// PHP havuzu isteğe bağlı: DevBox'ın kendi sunucusu statik dosyaları
	// PHP olmadan da sunabiliyor. Kurulu değilse uyarıp devam ediyoruz;
	// bir .php isteği geldiğinde işleyici ne yapılacağını söylüyor.
	//
	// Apache ve Nginx sürücülerinde durum farklı: üretilen yapılandırma
	// FastCGI adreslerini içeriyor ve havuz olmadan sunucu açılamıyor.
	if err := u.startPHP(ctx, phpPath); err != nil {
		if cfg.Server != project.ServerDevBox {
			return nil, err
		}
		u.logger.Warn("PHP havuzu başlatılamadı; yalnız statik dosyalar sunulacak",
			"hata", err)
		u.phpError = err
	}

	if cfg.Server == project.ServerDevBox {
		var pool web.Requester
		// Tip bilgisi olan nil bir arayüz, arayüzün kendisi nil olmadığı
		// için işleyicideki denetimi atlatırdı.
		if u.pool != nil {
			pool = u.pool
		}
		return &web.Handler{
			Pool:            pool,
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
		Env:         envList(u.projectEnv()),
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
	// Denetçi yan servislerle paylaşılıyor; varsa yenisini kurup
	// üstüne yazmıyoruz — yoksa servisler denetimsiz kalırdı.
	if err := u.ensureSupervisor(); err != nil {
		return err
	}

	for name, command := range u.cfg.Processes {
		parts, err := splitArgs(command)
		if err != nil || len(parts) == 0 {
			return fmt.Errorf("%s süreci çözümlenemedi: %q", name, command)
		}
		svc, err := u.sup.Add(supervisor.Config{
			Name:    name,
			Exec:    parts[0],
			Args:    parts[1:],
			WorkDir: u.cfg.Dir(),
			Env:     envList(u.projectEnv()),
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

// startMail, posta yakalayıcıyı açar ve arayüzünü kenara bağlar.
//
// Yakalayıcı tüm makine için tek: SMTP portu ortak, çünkü uygulamalar
// adresi yapılandırmalarına yazıyor. Aynı anda ikinci bir "devbox up"
// çalışıyorsa port dolu olur; bu bir hata değil — o oturum postayı zaten
// yakalıyor. Uyarıp devam ediyoruz, proje yine ayakta kalıyor.
func (u *upSession) startMail(e *edge.Edge) {
	if u.cfg.Mail.Disabled {
		return
	}
	addr, err := u.mailAddr()
	if err != nil {
		u.logger.Warn("posta yakalayıcı için adres bulunamadı; posta yakalanmayacak", "hata", err)
		return
	}
	capacity := u.cfg.Mail.Capacity
	if capacity == 0 {
		capacity = mail.DefaultCapacity
	}

	store := mail.NewStore(capacity)
	srv := &mail.SMTPServer{Addr: addr, Store: store, Logger: u.logger}

	if cfgRelay := u.cfg.Mail.Relay; cfgRelay != nil {
		relay := &mail.Relayer{
			Host:     cfgRelay.Host,
			Username: cfgRelay.Username,
			Allow:    cfgRelay.Allow,
			Logger:   u.logger,
		}
		if cfgRelay.PasswordEnv != "" {
			relay.Password = os.Getenv(cfgRelay.PasswordEnv)
			if relay.Password == "" {
				u.logger.Warn("röle parolası ortamda yok; röle kapalı",
					"değişken", cfgRelay.PasswordEnv)
			}
		}
		if relay.Password != "" || cfgRelay.Username == "" {
			if err := relay.Validate(); err != nil {
				u.logger.Warn("röle yapılandırması kullanılamadı; röle kapalı", "hata", err)
			} else {
				srv.Relay = relay
				u.relayTo = relay.Allow
			}
		}
	}
	if err := srv.Start(); err != nil {
		u.logger.Warn("posta yakalayıcı başlatılamadı; posta yakalanmayacak",
			"adres", addr, "hata", err)
		return
	}
	u.mailSMTP = srv
	// Kenar 80/443'ü tüm arayüzlerde dinliyor (siteyi telefondan
	// denemek için). Posta kutusu yakalanmış postaları gösteriyor;
	// o ağa açılmamalı.
	e.Handle(u.cfg.MailHost(), edge.LoopbackOnly(
		&mail.Handler{Store: store, SMTPAddr: srv.ListenAddr()}))
	u.localHosts = append(u.localHosts, u.cfg.MailHost())
}

// mailAddr, posta yakalayıcının dinleyeceği adresi seçer.
//
// Port ayırıcıdan geçiyor çünkü artık birden çok proje aynı anda
// çalışıyor ve hepsi 1025'i isteyecek. Eskiden ikincisi "başlatılamadı"
// deyip geçiyordu: uyarı vardı ama o projenin postaları hiç
// yakalanmıyordu. Ayırıcı 1025'ten yukarı tarayarak boş bir port
// bulduğu için ilk proje alışılmış portu alıyor, sonrakiler 1026, 1027…
//
// Uygulamanın doğru portu bulması projectEnv'e bağlı: MAIL_PORT oradan
// geliyor ve artık PHP havuzu da onu alıyor.
func (u *upSession) mailAddr() (string, error) {
	addr := u.cfg.Mail.SMTP
	if addr == "" {
		addr = mail.DefaultSMTPAddr
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("posta adresi \"host:port\" olmalı: %q", addr)
	}
	preferred, err := strconv.Atoi(portStr)
	if err != nil {
		return "", fmt.Errorf("posta portu sayı olmalı: %q", portStr)
	}

	port, err := u.alloc.Allocate(preferred)
	if err != nil {
		return "", err
	}
	// Ayırıcı denemek için bağlanıp bıraktı; asıl dinleyiciyi az sonra
	// SMTP sunucusu açacak. Rezervasyonu bırakmıyoruz ki aynı süreçte
	// başka bir bileşen aynı portu istemesin.
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// projectEnv, süreçlere ve zamanlanmış görevlere verilecek ortam
// değişkenlerini toplar: posta yakalayıcı ve yan servisler.
//
// Kullanıcının devbox.yaml'da yazdığı değer en sona konuyor, yani üste
// yazılmıyor: açıkça yazılmış bir ayarı sessizce değiştirmek en can
// sıkıcı davranış.
func (u *upSession) projectEnv() map[string]string {
	env := make(map[string]string, len(u.cfg.Env)+8)
	if u.mailSMTP != nil {
		host, port, err := net.SplitHostPort(u.mailSMTP.ListenAddr())
		if err == nil {
			env["MAIL_MAILER"] = "smtp"
			env["MAIL_HOST"] = host
			env["MAIL_PORT"] = port
		}
	}
	if u.svcManager != nil {
		for k, v := range u.svcManager.Env() {
			env[k] = v
		}
	}
	for k, v := range u.cfg.Env {
		env[k] = v
	}
	return env
}

// ensureSupervisor, denetçiyi bir kez kurar.
func (u *upSession) ensureSupervisor() error {
	if u.sup != nil {
		return nil
	}
	sup, err := supervisor.New(u.logger)
	if err != nil {
		return err
	}
	u.sup = sup
	return nil
}

// startInspector, HTTP denetleyicisini kurar ve kenarı ona sarar.
//
// Kayıt, kenarın en dışında duruyor: TLS sonlandıktan sonra ama
// yönlendirmeden önce. Böylece isteği uygulamanın gördüğü hâliyle
// kaydediyoruz — kenarın eklediği başlıklar dahil.
func (u *upSession) startInspector(e *edge.Edge, store *certs.Store, httpsAddr string) {
	if u.cfg.Inspect.Disabled {
		return
	}
	recorder := inspect.NewRecorder(u.cfg.Inspect.Capacity, 0)
	recorder.SetEnabled(true)
	u.inspector = recorder

	_, port := splitPort(httpsAddr)
	if port == "" {
		port = "443"
	}
	// Denetleyici kaydedilen istek gövdelerini ve Authorization
	// başlıklarını gösteriyor; yalnız makinenin kendisinden açılabilir.
	e.Handle(u.cfg.InspectHost(), edge.LoopbackOnly(&inspect.Handler{
		Recorder:  recorder,
		EdgeAddr:  "127.0.0.1:" + port,
		TLSConfig: &tls.Config{RootCAs: store.RootPool()},
		Domain:    u.cfg.Domain,
	}))
	u.localHosts = append(u.localHosts, u.cfg.InspectHost())
	store.Certificate(u.cfg.InspectHost())
}

// startServices, devbox.yaml'daki yan servisleri ayağa kaldırır.
//
// Servisler süreçlerden önce başlıyor: kuyruk işçisi Redis'i hazır
// bulmalı, aksi hâlde açılışta bağlanamayıp yeniden başlatma döngüsüne
// giriyor.
func (u *upSession) startServices(ctx context.Context, e *edge.Edge) error {
	if len(u.cfg.Services) == 0 {
		return nil
	}
	if err := u.ensureSupervisor(); err != nil {
		return err
	}

	manager := &services.Manager{
		Root:       filepath.Join(paths.DataDir(), "services", u.cfg.Name),
		Supervisor: u.sup,
		Alloc:      u.alloc,
		Logger:     u.logger,
	}
	for _, entry := range u.cfg.Services {
		if entry.Driver == project.DriverDocker {
			if err := u.startContainer(ctx, e, entry); err != nil {
				return err
			}
			continue
		}
		spec, err := services.ParseSpec(entry.Kind)
		if err != nil {
			return err
		}
		if _, err := manager.Start(ctx, spec); err != nil {
			return err
		}
	}
	u.svcManager = manager
	return nil
}

// startContainer, bir servisi konteynerde çalıştırır ve alan adı
// verilmişse kenar vekilini ona yönlendirir.
func (u *upSession) startContainer(ctx context.Context, e *edge.Edge, svc project.ServiceSpec) error {
	runtime, err := container.FindRuntime()
	if err != nil {
		return err
	}
	if !container.ImageExists(ctx, runtime, svc.Image) {
		u.logger.Info("imaj yerelde yok, indiriliyor", "imaj", svc.Image)
		if err := container.Pull(ctx, runtime, svc.Image); err != nil {
			return err
		}
	}

	hostPort, err := u.alloc.Allocate(0)
	if err != nil {
		return fmt.Errorf("%s servisi için port bulunamadı: %w", svc.Name, err)
	}

	spec := container.Spec{
		Project:       u.cfg.Name,
		Name:          svc.Name,
		Image:         svc.Image,
		ContainerPort: svc.Port,
		HostPort:      hostPort,
		Env:           svc.Env,
		Volumes:       svc.Volumes,
		Command:       svc.Command,
		WorkDir:       u.cfg.Dir(),
	}

	// Önceki oturumdan kalmış olabilir: aynı adla ikinci konteyner
	// açılamaz ve hata mesajı ("name already in use") kullanıcıya bir
	// şey anlatmaz.
	removeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	container.Remove(removeCtx, runtime, spec.ContainerName())
	cancel()

	service, err := u.sup.Add(container.ServiceConfig(runtime, spec))
	if err != nil {
		return err
	}
	if err := service.Start(ctx); err != nil {
		return fmt.Errorf("%s konteyneri başlatılamadı: %w", svc.Name, err)
	}

	u.containers = append(u.containers, containerRef{runtime: runtime, spec: spec, domain: svc.Domain})

	if svc.Domain != "" {
		handler, err := edge.ProxyHandler(svc.Domain, spec.Endpoint(), u.logger)
		if err != nil {
			return err
		}
		e.Handle(svc.Domain, handler)
		// Sertifika şimdi üretiliyor: ilk istekte üretmek, tarayıcının
		// ilk el sıkışmasını yavaşlatıyor.
		if store, err := certs.Open(paths.CertsDir()); err == nil {
			store.Certificate(svc.Domain)
		}
	}
	return nil
}

// startCron, devbox.yaml'daki zamanlanmış görevleri başlatır.
func (u *upSession) startCron(ctx context.Context) error {
	if len(u.cfg.Cron) == 0 {
		return nil
	}
	runner := &cron.Runner{
		Logger:  u.logger,
		WorkDir: u.cfg.Dir(),
		Env:     envList(u.projectEnv()),
	}
	for i, entry := range u.cfg.Cron {
		schedule, err := cron.Parse(entry.Schedule)
		if err != nil {
			return fmt.Errorf("%d. cron girdisi: %w", i+1, err)
		}
		parts, err := splitArgs(entry.Run)
		if err != nil || len(parts) == 0 {
			return fmt.Errorf("%d. cron girdisi çözümlenemedi: %q", i+1, entry.Run)
		}
		name := entry.Run
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		if err := runner.Add(cron.Job{
			Name: name, Schedule: schedule, Exec: parts[0], Args: parts[1:],
		}); err != nil {
			return err
		}
	}
	if err := runner.Start(ctx); err != nil {
		return err
	}
	u.cronRunner = runner
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
	} else if u.phpError != nil {
		fmt.Printf("  php       : yok — yalnız statik dosyalar\n")
		fmt.Printf("              kurmak için: devbox runtime install php@8.3\n")
	}
	if u.confPath != "" {
		fmt.Printf("  yapılandırma: %s\n", u.confPath)
	}
	if u.dnsServer != nil {
		fmt.Printf("  çözücü    : %s\n", u.dnsServer.Addr())
	}
	if u.mailSMTP != nil {
		fmt.Printf("  posta     : smtp %s, kutu https://%s\n",
			u.mailSMTP.ListenAddr(), u.cfg.MailHost())
	}
	if u.inspector != nil {
		fmt.Printf("  denetleyici: https://%s\n", u.cfg.InspectHost())
	}
	if len(u.relayTo) > 0 {
		fmt.Printf("  röle      : gerçek gönderim açık → %s\n", strings.Join(u.relayTo, ", "))
	}
	if u.svcManager != nil {
		for _, svc := range u.svcManager.Started() {
			fmt.Printf("  servis    : %s\n", svc.Summary())
		}
	}
	for _, ref := range u.containers {
		line := fmt.Sprintf("  konteyner : %s (%s) → 127.0.0.1:%d",
			ref.spec.Name, ref.spec.Image, ref.spec.HostPort)
		if ref.domain != "" {
			line += fmt.Sprintf(", https://%s", ref.domain)
		}
		fmt.Println(line)
	}
	if u.sup != nil {
		for _, s := range u.sup.Status() {
			// Yan servisler yukarıda kendi satırlarında listelendi.
			if strings.HasPrefix(s.Name, "servis-") {
				continue
			}
			fmt.Printf("  süreç     : %s (%s)\n", s.Name, s.State)
		}
	}
	if u.cronRunner != nil {
		for _, s := range u.cronRunner.Status() {
			fmt.Printf("  zamanlı   : %s\n", s.Name)
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

// hostOnly, Host başlığından portu atar.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// inspectorWrap, denetleyicinin kayıt katmanını üretir. Denetleyici
// kapalıysa nil döner ve kenar hiçbir şey sarmaz.
func (u *upSession) inspectorWrap() func(http.Handler) http.Handler {
	if u.inspector == nil {
		return nil
	}
	inspectHost := u.cfg.InspectHost()
	recorder := u.inspector
	return func(next http.Handler) http.Handler {
		recorded := recorder.Middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Denetleyicinin kendi trafiği kaydedilmiyor: her yoklama
			// yeni bir kayıt üretir, o kayıt akışa düşer, arayüz
			// yeniden yoklar — kendini besleyen bir döngü.
			if hostOnly(r.Host) == inspectHost {
				next.ServeHTTP(w, r)
				return
			}
			recorded.ServeHTTP(w, r)
		})
	}
}

// serveInternal, projeyi paylaşılan kenarın arkasında sunar.
//
// Burada 80/443 açılmıyor ve TLS sonlandırılmıyor; ikisi de çekirdek
// sürecin işi. Bunun sebebi mimari: her proje kendi kenarını açarsa
// ikinci proje 80'i alamaz ve aynı anda tek site çalışabilir — oysa
// birden çok siteyi yan yana çalıştırmak bu aracın var oluş sebebi.
func (u *upSession) serveInternal(
	ctx context.Context,
	cancel context.CancelFunc,
	e *edge.Edge,
	wrap func(http.Handler) http.Handler,
	addr string,
) error {
	if err := requireLoopback(addr); err != nil {
		return err
	}

	var h http.Handler = e
	if wrap != nil {
		h = wrap(h)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return bindHatasi(ctx, &edge.BindError{Addr: addr, Port: portOfAddr(addr), Err: err})
	}

	public := make([]string, 0, 1+len(u.cfg.Aliases))
	public = append(public, u.cfg.Domain)
	public = append(public, u.cfg.Aliases...)

	line, err := projects.FormatEndpoint(projects.Endpoint{
		Addr:      ln.Addr().String(),
		Hosts:     public,
		LocalOnly: u.localHosts,
	})
	if err != nil {
		ln.Close()
		return err
	}
	// Bildirim, hazır satırından ÖNCE yazılıyor: çekirdek süreç projeyi
	// hazır sayar saymaz adresi arıyor, sonra yazarsak bulamaz.
	fmt.Println(line)

	fmt.Printf("\n  %s%s%s\n\n", u.cfg.Name, projects.ReadyLine, u.cfg.Domain)
	u.printSummary()

	srv := &http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		srv.Close()
		return err
	case <-ctx.Done():
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		return srv.Shutdown(shutdownCtx)
	}
}

// requireLoopback, iç sunucunun yalnız geri döngüde açılmasını zorunlu
// kılar.
//
// Bu bir güvenlik denetimi, kolaylık değil: iç sunucu düz HTTP konuşuyor
// ve posta kutusu ile denetleyicinin "yalnız yerel" kısıtı paylaşılan
// kenarda uygulanıyor. Adres ağa açılırsa hem şifresiz trafik hem de
// kısıtsız posta kutusu dışarıya çıkar.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-internal adresi \"host:port\" olmalı: %q", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf(
			"-internal yalnız geri döngü adresi kabul eder (127.0.0.1 ya da ::1), verilen: %q\n"+
				"Bu adres düz HTTP konuşuyor ve posta kutusu kısıtı paylaşılan kenarda uygulanıyor;\n"+
				"ağa açılması ikisini birden dışarı taşır.", addr)
	}
	return nil
}

// portOfAddr, "host:port" biçiminden port numarasını çıkarır.
func portOfAddr(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}
