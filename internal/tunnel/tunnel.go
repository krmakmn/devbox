// Package tunnel, yerel bir siteyi geçici olarak internete açar.
//
// # Ne işe yarıyor
//
// "Şunu bir bak" demek için siteyi bir yere kurmak zorunda kalmamak.
// Webhook denemeleri (Stripe, GitHub, bir ödeme sağlayıcısı) bunu
// zorunlu kılıyor: sağlayıcı sizin makinenize istek atamaz.
//
// # Neden kendi tünel sunucumuz yok
//
// Tünel, internete açık bir sunucu gerektiriyor: birinin onu işletmesi,
// ödemesi ve güvenliğini üstlenmesi lazım. DevBox yerel bir araç; böyle
// bir hizmeti işletmek projenin doğasını değiştirirdi. Onun yerine
// kullanıcının seçtiği sağlayıcının kendi aracı çalıştırılıyor —
// cloudflared ya da ngrok. İkisi de ücretsiz katmanda çalışıyor.
//
// # Uyarı neden var
//
// Tünel açmak, geliştirme makinenizdeki siteyi internete açmak demek:
// hata ayıklama araçları, denetleyici, posta kutusu ve genellikle
// kapatılmamış hata mesajları dahil. Komut bunu her seferinde
// söylüyor ve yalnız proje alan adını yayımlıyor.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Provider, tünel sağlayıcısı.
type Provider struct {
	// Name, komut satırında yazılan ad.
	Name string

	// Binary, aranacak çalıştırılabilir.
	Binary string

	// InstallHint, bulunamazsa gösterilecek yol.
	InstallHint string

	// args, hedef adres verildiğinde komutu üretir.
	args func(target string) []string

	// urlPattern, çıktıdan genel adresi çıkarır.
	urlPattern *regexp.Regexp
}

var providers = []Provider{
	{
		Name:        "cloudflared",
		Binary:      "cloudflared",
		InstallHint: "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
		args: func(target string) []string {
			return []string{"tunnel", "--no-autoupdate", "--url", target}
		},
		// cloudflared adresi stderr'e bir kutu içinde yazıyor.
		urlPattern: regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`),
	},
	{
		Name:        "ngrok",
		Binary:      "ngrok",
		InstallHint: "https://ngrok.com/download",
		args: func(target string) []string {
			return []string{"http", target, "--log", "stdout", "--log-format", "logfmt"}
		},
		urlPattern: regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.ngrok(-free)?\.(app|io)`),
	},
}

// Providers, desteklenen sağlayıcılar.
func Providers() []Provider { return append([]Provider(nil), providers...) }

// Find, adı verilen sağlayıcıyı bulur. Ad boşsa kurulu olan ilkini
// seçer.
func Find(name string) (Provider, error) {
	if name != "" {
		for _, p := range providers {
			if strings.EqualFold(p.Name, name) {
				if _, err := exec.LookPath(p.Binary); err != nil {
					return Provider{}, fmt.Errorf(
						"tunnel: %s bulunamadı.\n  Kurulum: %s", p.Binary, p.InstallHint)
				}
				return p, nil
			}
		}
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			names = append(names, p.Name)
		}
		return Provider{}, fmt.Errorf("tunnel: bilinmeyen sağlayıcı %q (%s)",
			name, strings.Join(names, ", "))
	}

	for _, p := range providers {
		if _, err := exec.LookPath(p.Binary); err == nil {
			return p, nil
		}
	}
	var hints strings.Builder
	for _, p := range providers {
		fmt.Fprintf(&hints, "\n  %-12s %s", p.Name, p.InstallHint)
	}
	return Provider{}, fmt.Errorf("tunnel: tünel aracı bulunamadı. Şunlardan biri kurulmalı:%s", hints.String())
}

// Tunnel, çalışan bir tünel.
type Tunnel struct {
	Provider Provider

	cmd    *exec.Cmd
	mu     sync.Mutex
	url    string
	found  chan struct{}
	closed bool
}

// Start, tüneli açar.
//
// target, yerel adres ("https://127.0.0.1:443"). Yayımlanan alan adı
// isteğin Host başlığında taşınmıyor; sağlayıcı kendi adını kullanıyor.
// Bu yüzden kenarın o adı da tanıması gerekiyor — çağıran onu ekliyor.
func (p Provider) Start(ctx context.Context, target string, out io.Writer) (*Tunnel, error) {
	binary, err := exec.LookPath(p.Binary)
	if err != nil {
		return nil, fmt.Errorf("tunnel: %s bulunamadı.\n  Kurulum: %s", p.Binary, p.InstallHint)
	}

	cmd := exec.CommandContext(ctx, binary, p.args(target)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tunnel: %s başlatılamadı: %w", p.Binary, err)
	}

	t := &Tunnel{Provider: p, cmd: cmd, found: make(chan struct{})}
	go t.scan(stdout, out)
	go t.scan(stderr, out)
	return t, nil
}

// scan, aracın çıktısını okuyup genel adresi arar.
func (t *Tunnel) scan(r io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if out != nil {
			fmt.Fprintln(out, line)
		}
		if match := t.Provider.urlPattern.FindString(line); match != "" {
			t.mu.Lock()
			if t.url == "" {
				t.url = match
				close(t.found)
			}
			t.mu.Unlock()
		}
	}
}

// URL, tünelin genel adresini bekler.
func (t *Tunnel) URL(ctx context.Context, timeout time.Duration) (string, error) {
	select {
	case <-t.found:
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.url, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(timeout):
		return "", fmt.Errorf("tunnel: %s adresi %s içinde bildirmedi", t.Provider.Name, timeout)
	}
}

// Wait, aracın bitmesini bekler.
func (t *Tunnel) Wait() error { return t.cmd.Wait() }

// Close, tüneli kapatır.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
	t.cmd.Wait()
	return nil
}

// HostOf, bir adresten konak adını çıkarır.
func HostOf(url string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexAny(trimmed, "/:"); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}
