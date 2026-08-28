package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

func TestMain(m *testing.M) {
	if os.Getenv("DEVBOX_FAKE_SERVICE") == "1" {
		// Sonsuza kadar yaşayan, düzenli çıktı üreten sahte bir servis.
		for i := 0; ; i++ {
			fmt.Printf("satır %d\n", i)
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

// testServer, dolu bir denetçiyle API sunucusu kurar ve istemcisini döner.
func testServer(t *testing.T, withService bool) (*Client, *supervisor.Supervisor) {
	t.Helper()

	sup, err := supervisor.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sup.Close() })

	if withService {
		_, err := sup.Add(supervisor.Config{
			Name:         "sahte",
			Exec:         os.Args[0],
			Env:          []string{"DEVBOX_FAKE_SERVICE=1"},
			StartTimeout: 15 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{Token: token, Supervisor: sup})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	return NewClient(addr, token), sup
}

func TestStatusAndServices(t *testing.T) {
	client, _ := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.APIVersion != Version {
		t.Errorf("API sürümü %q, beklenen %q", status.APIVersion, Version)
	}
	if status.Services != 1 {
		t.Errorf("servis sayısı %d, beklenen 1", status.Services)
	}

	services, err := client.Services(ctx)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(services) != 1 || services[0].Name != "sahte" {
		t.Errorf("servis listesi = %+v", services)
	}
}

func TestStartStopThroughAPI(t *testing.T) {
	client, _ := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := client.StartService(ctx, "sahte")
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	if st.State != "çalışıyor" {
		t.Errorf("başlatmadan sonra durum %q", st.State)
	}
	if st.PID == 0 {
		t.Error("pid bildirilmedi")
	}

	st, err = client.StopService(ctx, "sahte")
	if err != nil {
		t.Fatalf("StopService: %v", err)
	}
	if st.State != "durdu" {
		t.Errorf("durdurmadan sonra durum %q", st.State)
	}
}

func TestUnknownServiceIsNotFound(t *testing.T) {
	client, _ := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.StartService(ctx, "yok")
	if err == nil {
		t.Fatal("olmayan servis için hata dönmedi")
	}
	// Hata sunucunun mesajını taşımalı; çıplak "404" kullanıcıya bir şey
	// anlatmıyor.
	if !strings.Contains(err.Error(), "yok") {
		t.Errorf("hata servisin adını içermiyor: %v", err)
	}
}

// --- güvenlik --------------------------------------------------------------

func TestRejectsMissingOrWrongToken(t *testing.T) {
	client, _ := testServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Jetonsuz istek.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("jetonsuz istek durumu %d, beklenen 401", resp.StatusCode)
	}

	// Yanlış jeton.
	wrong := NewClient(strings.TrimPrefix(client.baseURL, "http://"), "yanlış-jeton")
	if _, err := wrong.Status(ctx); err == nil {
		t.Error("yanlış jetonla istek kabul edildi")
	}
}

// DNS yeniden bağlama: saldırgan kendi alan adını 127.0.0.1'e çözdürüp
// kurbanın tarayıcısından bize istek attırabilir. Tarayıcı bunu aynı köken
// saydığı için engellemez; Host başlığını denetlemek keser.
func TestRejectsForeignHostHeader(t *testing.T) {
	client, _ := testServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Host = "saldirgan.example"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("yabancı Host başlığı durumu %d, beklenen 403", resp.StatusCode)
	}
}

func TestAcceptsLoopbackHostVariants(t *testing.T) {
	client, _ := testServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, host := range []string{"127.0.0.1:1234", "localhost:1234", "LOCALHOST", "[::1]:1234"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+client.token)
		req.Host = host

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q durumu %d, beklenen 200", host, resp.StatusCode)
		}
	}
}

// --- jeton dosyası ---------------------------------------------------------

func TestLoadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 40 {
		t.Errorf("jeton çok kısa: %d karakter", len(first))
	}

	second, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("ikinci çağrıda yeni jeton üretildi; çalışan istemciler yetkisiz kalırdı")
	}

	// Başka bir kullanıcı jetonu okuyup servisleri yönetebilmemeli.
	if info, err := os.Stat(path); err == nil && os.PathSeparator == '/' {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("jeton dosyası izinleri çok geniş: %v", perm)
		}
	}
}

func TestGeneratedTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[token] {
			t.Fatal("aynı jeton iki kez üretildi")
		}
		seen[token] = true
	}
}

func TestEndpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.endpoint")
	if err := WriteEndpoint(path, "127.0.0.1:9876"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:9876" {
		t.Errorf("adres = %q", got)
	}
	if _, err := ReadEndpoint(filepath.Join(t.TempDir(), "yok")); err == nil {
		t.Error("olmayan dosya için hata dönmedi")
	}
}

// --- günlük akışı ----------------------------------------------------------

func TestLogsAndStream(t *testing.T) {
	client, _ := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := client.StartService(ctx, "sahte"); err != nil {
		t.Fatal(err)
	}
	// Sahte servis 100 ms'de bir satır yazıyor.
	time.Sleep(400 * time.Millisecond)

	logs, err := client.Logs(ctx, "sahte")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(logs, "satır 0") {
		t.Errorf("birikmiş günlük beklenen satırı içermiyor:\n%s", logs)
	}

	// Canlı akış: hem birikmişi hem yeni satırları vermeli.
	streamCtx, streamCancel := context.WithTimeout(ctx, 5*time.Second)
	defer streamCancel()

	var mu sync.Mutex
	var lines []string
	done := make(chan error, 1)
	go func() {
		done <- client.StreamLogs(streamCtx, "sahte", func(line string) {
			mu.Lock()
			lines = append(lines, line)
			if len(lines) >= 8 {
				streamCancel()
			}
			mu.Unlock()
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("akış zamanında bitmedi")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 5 {
		t.Errorf("akıştan %d satır geldi, daha fazlası bekleniyordu: %v", len(lines), lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "satır") {
		t.Errorf("akış beklenen içeriği taşımıyor: %v", lines)
	}
}

func TestStreamOfUnknownServiceFails(t *testing.T) {
	client, _ := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := client.StreamLogs(ctx, "yok", func(string) {})
	if err == nil {
		t.Error("olmayan servisin akışı hata vermedi")
	}
}

func TestRuntimesEndpointWithoutStore(t *testing.T) {
	client, _ := testServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runtimes, err := client.Runtimes(ctx)
	if err != nil {
		t.Fatalf("Runtimes: %v", err)
	}
	if len(runtimes) != 0 {
		t.Errorf("runtime listesi boş değil: %+v", runtimes)
	}
}

func TestWaitReady(t *testing.T) {
	client, _ := testServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.WaitReady(ctx, 5*time.Second); err != nil {
		t.Errorf("WaitReady: %v", err)
	}

	// Kimsenin dinlemediği bir adres.
	dead := NewClient("127.0.0.1:1", "jeton")
	if err := dead.WaitReady(ctx, 300*time.Millisecond); err == nil {
		t.Error("ölü adres için hata dönmedi")
	}
}
