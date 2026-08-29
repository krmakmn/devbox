package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/krmakmn/devbox/internal/project"
	"github.com/krmakmn/devbox/internal/tunnel"
)

// runShare, çalışan bir projeyi geçici olarak internete açar.
func runShare(args []string) error {
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	var (
		dir      = fs.String("dir", ".", "proje dizini")
		provider = fs.String("provider", "", "tünel sağlayıcısı: cloudflared ya da ngrok (boşsa kurulu olan)")
		target   = fs.String("target", "https://127.0.0.1:443", "yerel adres")
		quiet    = fs.Bool("quiet", false, "tünel aracının çıktısını gizle")
		list     = fs.Bool("list", false, "sağlayıcıları listele ve çık")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox share [seçenekler]

Çalışan projeyi geçici bir genel adresle internete açar. Webhook
denemeleri (Stripe, GitHub) ve "şunu bir bak" için.

Önce "devbox up" ile projeyi ayağa kaldırın, sonra bu komutu çalıştırın.
Ctrl+C tüneli kapatır.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *list {
		fmt.Println("Desteklenen tünel sağlayıcıları:")
		for _, p := range tunnel.Providers() {
			fmt.Printf("  %-12s %s\n", p.Name, p.InstallHint)
		}
		return nil
	}

	cfg, err := project.Load(mustAbs(*dir))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s bulunamadı; önce \"devbox init\" çalıştırın", project.FileName)
		}
		return err
	}

	p, err := tunnel.Find(*provider)
	if err != nil {
		return err
	}

	// Uyarı her seferinde: tünel açmak, geliştirme makinesindeki siteyi
	// hata ayıklama araçlarıyla birlikte internete açmak demek.
	fmt.Printf("\n  ⚠ %s internete açılıyor (%s ile).\n", cfg.Domain, p.Name)
	fmt.Printf("    Bu adres herkese açık. Denetleyici, posta kutusu ve\n")
	fmt.Printf("    ayrıntılı hata mesajları da erişilebilir olabilir.\n")
	fmt.Printf("    Ctrl+C ile kapatın.\n\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out *os.File
	if !*quiet {
		out = os.Stderr
	}
	t, err := p.Start(ctx, *target, out)
	if err != nil {
		return err
	}
	defer t.Close()

	url, err := t.URL(ctx, 60*time.Second)
	if err != nil {
		return err
	}

	fmt.Printf("\n  Genel adres: %s\n", url)
	fmt.Printf("  Yerel hedef: %s (%s)\n\n", *target, cfg.Domain)
	fmt.Printf("  Not: kenar vekili istekleri konak adına göre dağıtıyor.\n")
	fmt.Printf("  Tünelden gelen istekler %s konağıyla geliyor; projenin\n", tunnel.HostOf(url))
	fmt.Printf("  bunu tanıması için devbox.yaml'daki aliases listesine ekleyin:\n\n")
	fmt.Printf("    aliases:\n      - %s\n\n", tunnel.HostOf(url))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\ntünel kapatılıyor…")
	return nil
}

// mustAbs, yolu mutlak hâle getirir; olmazsa verileni döner.
func mustAbs(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}
