package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/krmakmn/devbox/internal/acme"
	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/paths"
)

// mapList, tekrarlanabilir -map bayrağını toplar.
type mapList map[string]string

func (m mapList) String() string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ", ")
}

func (m mapList) Set(v string) error {
	domain, addr, ok := strings.Cut(v, "=")
	if !ok || domain == "" || addr == "" {
		return fmt.Errorf("eşleme alan=adres:port biçiminde olmalı: %q", v)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("%q geçerli bir adres:port değil", addr)
	}
	m[domain] = addr
	return nil
}

func runACME(args []string) error {
	fs := flag.NewFlagSet("acme", flag.ContinueOnError)
	addr := fs.String("addr", acme.DefaultAddr, "dinleme adresi")
	overrides := mapList{}
	fs.Var(overrides, "map", "doğrulama yönlendirmesi: alan=adres:port (birden çok kez verilebilir)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox acme serve [seçenekler]

Yerel bir ACME sunucusu çalıştırır. Konteynerde ya da WSL2'de koşan
Caddy, Traefik, certbot gibi istemciler sertifikalarını buradan alır;
kök sertifika zaten güven depolarında olduğu için tarayıcı uyarmaz.

  devbox acme serve
  → ACME_CA / --ca adresi olarak dizin adresini verin.

http-01 doğrulaması alan adının 80. portuna bağlanır. DevBox'ın kenar
vekili o portu tuttuğu için, istemcinin meydan okumayı sunduğu adresi
-map ile bildirin:

  devbox acme serve -map api.magaza.test=127.0.0.1:8080

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "serve")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if sub != "serve" {
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}

	store, err := certs.Open(paths.CertsDir())
	if err != nil {
		return fmt.Errorf("sertifika deposu açılamadı: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	srv, err := acme.New(acme.Config{
		Store:  store,
		Logger: logger,
		Resolve: func(domain string) (string, bool) {
			target, ok := overrides[domain]
			return target, ok
		},
	})
	if err != nil {
		return err
	}
	listenAddr, err := srv.Start(*addr)
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Printf("\n  Yerel ACME sunucusu hazır\n\n")
	fmt.Printf("  Dizin adresi : http://%s/acme/directory\n", listenAddr)
	fmt.Printf("  Kök sertifika: http://%s/acme/root.crt\n", listenAddr)
	if len(overrides) > 0 {
		fmt.Printf("  Doğrulama yönlendirmeleri: %s\n", overrides)
	}
	fmt.Printf("\n  Örnek (Caddy):\n    acme_ca http://%s/acme/directory\n", listenAddr)
	fmt.Printf("\n  Ctrl+C ile durdurun.\n")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\nkapatılıyor…")
	return nil
}
