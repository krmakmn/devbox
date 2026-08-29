package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/api"
	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/dns"
	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/procstat"
	"github.com/krmakmn/devbox/internal/projects"
	"github.com/krmakmn/devbox/internal/supervisor"
)

func tokenPath() string    { return filepath.Join(paths.DataDir(), "api.token") }
func endpointPath() string { return filepath.Join(paths.DataDir(), "api.endpoint") }

// serviceList, tekrarlanabilir -service bayrağını toplar.
type serviceList []string

func (s *serviceList) String() string { return strings.Join(*s, ", ") }

func (s *serviceList) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("servis ad=komut biçiminde olmalı: %q", v)
	}
	*s = append(*s, v)
	return nil
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	var services serviceList
	fs.Var(&services, "service", `ad=komut (birden çok kez verilebilir), ör. -service "mysql=mysqld --datadir=C:\veri"`)
	addr := fs.String("addr", "127.0.0.1:0", "API dinleme adresi (0 = işletim sistemi seçsin)")
	httpAddr := fs.String("http", ":80", "paylaşılan kenarın HTTP adresi")
	httpsAddr := fs.String("https", ":443", "paylaşılan kenarın HTTPS adresi")
	noEdge := fs.Bool("no-edge", false, "paylaşılan kenarı açma (projeler kendi portlarını açar)")
	noDNS := fs.Bool("no-dns", false, "yerel çözücüyü çalıştırma")
	dnsAddr := fs.String("dns", dns.DefaultAddr, "çözücünün dinleyeceği adres")
	suffix := fs.String("suffix", "test", "çözücünün sahipleneceği son ek")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox daemon [seçenekler]

Servisleri yöneten çekirdek süreci çalıştırır. Yönetim arayüzü yalnız
loopback'i dinler ve jeton ister; adres ve jeton veri dizinine yazılır,
devbox ps / logs onları oradan okur.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	sup, err := supervisor.New(logger)
	if err != nil {
		return err
	}
	defer sup.Close()

	for _, spec := range services {
		name, command, _ := strings.Cut(spec, "=")
		parts, err := splitArgs(command)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if len(parts) == 0 {
			return fmt.Errorf("%s: komut boş", name)
		}
		if _, err := sup.Add(supervisor.Config{
			Name: strings.TrimSpace(name),
			Exec: parts[0],
			Args: parts[1:],
		}); err != nil {
			return err
		}
	}

	// Projeler: kayıt her zaman açılıyor, çünkü denetim paneli boş bir
	// listeyle de anlamlı ("devbox project add" diyor).
	registry, err := openRegistry()
	if err != nil {
		return err
	}
	exe, err := devboxExecutable()
	if err != nil {
		return err
	}
	runner := &projects.Runner{
		Registry:   registry,
		Supervisor: sup,
		Executable: exe,
		Logger:     logger,
	}

	// Paylaşılan kenar: 80/443'ü tek bir dinleyici tutuyor ve istekleri
	// host adına göre projelere dağıtıyor. Her proje kendi kenarını
	// açsaydı ikincisi portu alamaz, aynı anda tek site çalışabilirdi —
	// oysa birden çok siteyi yan yana çalıştırmak bu aracın var oluş
	// sebebi.
	var edgeSrv *edge.Server
	if !*noEdge {
		store, err := certs.Open(paths.CertsDir())
		if err != nil {
			return fmt.Errorf("sertifika deposu açılamadı: %w", err)
		}
		shared := edge.New()
		shared.Logger = logger
		if _, port := splitPort(*httpsAddr); port != "" {
			shared.HTTPSPort = port
		}
		runner.Edge = shared

		edgeSrv = &edge.Server{
			Edge:      shared,
			HTTPAddr:  *httpAddr,
			HTTPSAddr: *httpsAddr,
			TLSConfig: store.TLSConfig(),
		}
		// Dinleyiciler API'den önce açılıyor: port alınamıyorsa
		// kullanıcıya bunu, panel adresini yazdırmadan önce söylemeliyiz.
		if err := edgeSrv.Listen(); err != nil {
			return bindHatasi(context.Background(), err)
		}
	}

	// Çözücü de çekirdeğin işi. Projeler paylaşılan kenar kipinde kendi
	// çözücülerini açmıyor; biri açsaydı ilki dışındakiler "adres
	// kullanımda" ile düşerdi.
	if !*noDNS && !*noEdge {
		resolver := dns.New(dns.Config{
			Addr:     *dnsAddr,
			Suffixes: []string{*suffix},
			Logger:   logger,
		})
		if err := resolver.Start(); err != nil {
			logger.Warn("yerel çözücü başlatılamadı; alan adı çözümlemesi elle ayarlanmalı",
				"hata", err)
		} else {
			defer resolver.Close()
		}
	}

	token, err := api.LoadOrCreateToken(tokenPath())
	if err != nil {
		return err
	}
	srv, err := api.NewServer(api.Config{
		Token:      token,
		Supervisor: sup,
		Runtimes:   runtimeStore(),
		Projects:   runner,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	listenAddr, err := srv.Start(*addr)
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := api.WriteEndpoint(endpointPath(), listenAddr); err != nil {
		return err
	}
	// Adres dosyası kalırsa, devboxd kapandıktan sonra devbox ps ölü bir
	// adrese bağlanmaya çalışır ve hata anlaşılmaz olur.
	defer os.Remove(endpointPath())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := sup.StartAll(ctx); err != nil {
		cancel()
		return err
	}
	cancel()

	logger.Info("devboxd başladı", "adres", listenAddr, "servis", len(sup.Status()))
	fmt.Printf("\n  Denetim paneli: http://%s/\n  Açmak için: devbox ui\n\n", listenAddr)

	edgeCtx, edgeCancel := context.WithCancel(context.Background())
	defer edgeCancel()
	edgeErr := make(chan error, 1)
	if edgeSrv != nil {
		go func() { edgeErr <- edgeSrv.Serve(edgeCtx) }()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case <-stop:
	case err := <-edgeErr:
		// Kenar düşerse çekirdeği ayakta tutmanın anlamı yok: hiçbir
		// site açılmaz ve kullanıcı sebebi göremez.
		if err != nil {
			logger.Error("paylaşılan kenar durdu", "hata", err)
			return err
		}
	}

	logger.Info("devboxd kapatılıyor")
	return nil
}

// dialDaemon, çalışan devboxd'ye bağlanan bir istemci döner.
func dialDaemon() (*api.Client, error) {
	addr, err := api.ReadEndpoint(endpointPath())
	if err != nil {
		return nil, fmt.Errorf("%w\n(devboxd çalışmıyor olabilir: devbox daemon)", err)
	}
	token, err := api.ReadToken(tokenPath())
	if err != nil {
		return nil, err
	}
	return api.NewClient(addr, token), nil
}

func runPS(args []string) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := dialDaemon()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	services, err := client.Services(ctx)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		fmt.Println("tanımlı servis yok")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVİS\tDURUM\tPID\tBELLEK\tSÜRE\tYENİDEN\tHAZIRLIK")
	for _, s := range services {
		pid, bellek := "-", "-"
		if s.PID != 0 {
			pid = fmt.Sprint(s.PID)
			// Çekirdek süreç ve CLI aynı makinede olduğu için ölçümü
			// buradan okuyabiliyoruz; API'ye yeni bir alan eklemeye
			// gerek yok.
			if usage, err := procstat.Read(s.PID); err == nil {
				bellek = humanBytes(usage.RSS)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			s.Name, s.State, pid, bellek,
			time.Since(s.Since).Round(time.Second), s.Restarts, s.Ready)
	}
	return w.Flush()
}

func runLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "canlı akışı izle (Ctrl+C ile çık)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Kullanım: devbox logs [-f] <servis>")
		fs.PrintDefaults()
	}
	// Servis adı bayraklardan önce de sonra da yazılabilsin: "logs -f web"
	// ve "logs web -f" ikisi de çalışmalı. flag paketi ilk konumsal
	// argümanda durduğu için ikinci Parse gerekiyor.
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if fs.NArg() > 1 {
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	if name == "" {
		fs.Usage()
		return errors.New("servis adı gerekli")
	}

	client, err := dialDaemon()
	if err != nil {
		return err
	}

	if !*follow {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logs, err := client.Logs(ctx, name)
		if err != nil {
			return err
		}
		fmt.Print(logs)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	return client.StreamLogs(ctx, name, func(line string) {
		fmt.Println(line)
	})
}

// splitArgs, komut satırını kabuk benzeri kurallarla parçalar.
//
// Tam bir kabuk ayrıştırıcısı değil; çift tırnak içindeki boşlukları
// koruyor, o kadar. Windows yollarındaki ters bölü kaçış karakteri
// sayılmıyor — "C:\php\php.exe" yazabilmek bunu gerektiriyor ve kaçış
// desteği için ödenecek bedel buna değmez.
func splitArgs(command string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		inQuote bool
		hasWord bool
	)

	for _, r := range command {
		switch {
		case r == '"':
			inQuote = !inQuote
			hasWord = true
		case (r == ' ' || r == '\t') && !inQuote:
			if hasWord {
				args = append(args, current.String())
				current.Reset()
				hasWord = false
			}
		default:
			current.WriteRune(r)
			hasWord = true
		}
	}
	if inQuote {
		return nil, errors.New("kapatılmamış tırnak")
	}
	if hasWord {
		args = append(args, current.String())
	}
	return args, nil
}

// humanBytes, bayt sayısını okunur biçime çevirir.
func humanBytes(b uint64) string {
	const birim = 1024
	if b < birim {
		return fmt.Sprintf("%d B", b)
	}
	d, exp := float64(b)/birim, 0
	for d >= birim && exp < 2 {
		d /= birim
		exp++
	}
	return fmt.Sprintf("%.1f %s", d, [...]string{"KB", "MB", "GB"}[exp])
}
