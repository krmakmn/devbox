package tunnel

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFindReportsMissingToolsWithInstallHints(t *testing.T) {
	// PATH'i boşaltarak hiçbir aracın bulunmadığı durumu kur.
	t.Setenv("PATH", t.TempDir())

	_, err := Find("")
	if err == nil {
		t.Fatal("araç yokken hata verilmedi")
	}
	for _, p := range Providers() {
		if !strings.Contains(err.Error(), p.InstallHint) {
			t.Errorf("hata %s kurulum yolunu söylemiyor: %v", p.Name, err)
		}
	}

	if _, err := Find("bilinmeyen"); err == nil {
		t.Error("bilinmeyen sağlayıcı kabul edildi")
	}
}

// Sağlayıcıların adres kalıpları gerçek araçların çıktısına uymalı.
// Buradaki satırlar araçların belgelerindeki biçimden alındı.
func TestURLPatternsMatchRealToolOutput(t *testing.T) {
	cases := map[string]struct {
		provider string
		line     string
		want     string
	}{
		"cloudflared kutusu": {
			provider: "cloudflared",
			line:     "2024-01-01T00:00:00Z INF |  https://random-words-here.trycloudflare.com   |",
			want:     "https://random-words-here.trycloudflare.com",
		},
		"ngrok logfmt": {
			provider: "ngrok",
			line:     `t=2024-01-01T00:00:00+0000 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=https://127.0.0.1:443 url=https://abc-12-34.ngrok-free.app`,
			want:     "https://abc-12-34.ngrok-free.app",
		},
		"ngrok eski alan": {
			provider: "ngrok",
			line:     "url=https://1234abcd.ngrok.io",
			want:     "https://1234abcd.ngrok.io",
		},
	}

	byName := map[string]*regexp.Regexp{}
	for _, p := range Providers() {
		byName[p.Name] = p.urlPattern
	}
	for label, c := range cases {
		got := byName[c.provider].FindString(c.line)
		if got != c.want {
			t.Errorf("%s: adres = %q, %q bekleniyordu", label, got, c.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://abc.trycloudflare.com":  "abc.trycloudflare.com",
		"https://abc.ngrok-free.app/yol": "abc.ngrok-free.app",
		"http://127.0.0.1:8080":          "127.0.0.1",
		"abc.example.com":                "abc.example.com",
	}
	for in, want := range cases {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, %q bekleniyordu", in, got, want)
		}
	}
}

// Tünel akışı: aracı başlat, çıktısından adresi yakala, kapat.
//
// Gerçek cloudflared/ngrok bu ortamda kurulamıyor (ağ kısıtlı), bu
// yüzden onların çıktı biçimini taklit eden sahte bir araç
// kullanılıyor. Sınanan şey akış ve ayrıştırma; tünelin kendisi değil.
func TestStartCapturesURLAndCloses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte araç kabuk betiği")
	}
	dir := t.TempDir()
	sahte := filepath.Join(dir, "cloudflared")
	script := "#!/bin/sh\n" +
		"echo 'INF Requesting new quick tunnel...' >&2\n" +
		"echo '|  https://sahte-tunel-123.trycloudflare.com  |' >&2\n" +
		"sleep 30\n"
	if err := os.WriteFile(sahte, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p, err := Find("cloudflared")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tun, err := p.Start(ctx, "https://127.0.0.1:443", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()

	url, err := tun.URL(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("adres yakalanamadı: %v", err)
	}
	if url != "https://sahte-tunel-123.trycloudflare.com" {
		t.Errorf("adres = %q", url)
	}

	// Kapatma askıda kalmamalı.
	done := make(chan struct{})
	go func() { tun.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close dönmedi")
	}
}

// Araç adresi hiç bildirmezse açık bir hata verilmeli; sessizce
// beklemek kullanıcıyı "takıldı mı?" diye bırakır.
func TestURLTimesOutWhenToolSaysNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte araç kabuk betiği")
	}
	dir := t.TempDir()
	sahte := filepath.Join(dir, "ngrok")
	if err := os.WriteFile(sahte, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p, _ := Find("ngrok")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tun, err := p.Start(ctx, "https://127.0.0.1:443", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()

	if _, err := tun.URL(ctx, 500*time.Millisecond); err == nil {
		t.Error("adres bildirilmedi ama hata verilmedi")
	}
}
