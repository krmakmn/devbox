package phppool

import (
	"context"
	"io"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"
)

func newTestPool(t *testing.T, cfg Config) *Pool {
	t.Helper()
	cfg.Exec = os.Args[0]
	cfg.Env = append(cfg.Env, "DEVBOX_FAKE_PHPCGI=1")
	if cfg.SpawnTimeout == 0 {
		cfg.SpawnTimeout = 15 * time.Second
	}

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func params(uri, query string) map[string]string {
	return map[string]string{
		"REQUEST_METHOD":  "GET",
		"SERVER_PROTOCOL": "HTTP/1.1",
		"REQUEST_URI":     uri,
		"QUERY_STRING":    query,
		"SCRIPT_NAME":     "/index.php",
		"HTTP_HOST":       "deneme.test",
		"CONTENT_LENGTH":  "0",
	}
}

func request(t *testing.T, p *Pool, query string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	uri := "/index.php"
	if query != "" {
		uri += "?" + query
	}
	resp, err := p.Do(ctx, params(uri, query), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		t.Errorf("durum %d, beklenen 200 (gövde: %s)", resp.StatusCode, body)
	}
	return string(body), nil
}

var pidPattern = regexp.MustCompile(`pid=(\d+)`)

func pidOf(t *testing.T, body string) int {
	t.Helper()
	m := pidPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("yanıtta pid yok: %q", body)
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

func TestPoolServesRequests(t *testing.T) {
	p := newTestPool(t, Config{Workers: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	for i := 0; i < 20; i++ {
		body, err := request(t, p, "")
		if err != nil {
			t.Fatalf("%d. istek: %v", i, err)
		}
		pidOf(t, body)
	}
	if got := p.Stats().Requests; got != 20 {
		t.Errorf("istek sayacı %d, beklenen 20", got)
	}
}

func TestPoolRunsRequestsInParallel(t *testing.T) {
	const workers = 4
	p := newTestPool(t, Config{Workers: workers})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}
	// Tüm işçiler ayağa kalksın.
	waitForIdle(t, p, workers)

	var mu sync.Mutex
	pids := map[int]bool{}

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := request(t, p, "sleep=300ms")
			if err != nil {
				t.Errorf("istek: %v", err)
				return
			}
			mu.Lock()
			pids[pidOf(t, body)] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if len(pids) != workers {
		t.Errorf("%d ayrı süreç kullanıldı, beklenen %d — istekler paralelleşmiyor", len(pids), workers)
	}
	// Seri çalışsaydı 4 x 300ms = 1.2s sürerdi.
	if elapsed > 900*time.Millisecond {
		t.Errorf("%d eşzamanlı istek %v sürdü; seri çalışıyor olabilir", workers, elapsed)
	}
}

func TestPoolRecyclesWorkerAfterMaxRequests(t *testing.T) {
	// Tek işçi + 2 istek sınırı: 6 istekten sonra en az iki yenilenme olmalı.
	p := newTestPool(t, Config{Workers: 1, MaxRequests: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	seen := map[int]bool{}
	for i := 0; i < 6; i++ {
		body, err := request(t, p, "")
		if err != nil {
			t.Fatalf("%d. istek: %v", i, err)
		}
		seen[pidOf(t, body)] = true
	}

	if len(seen) < 3 {
		t.Errorf("%d ayrı süreç görüldü, beklenen en az 3 — işçi yenilenmiyor", len(seen))
	}
	if got := p.Stats().Recycled; got < 2 {
		t.Errorf("yenilenme sayacı %d, beklenen en az 2", got)
	}
	// Yenilenme bir çökme olarak sayılmamalı.
	if got := p.Stats().Crashed; got != 0 {
		t.Errorf("çökme sayacı %d, beklenen 0", got)
	}
}

func TestPoolSurvivesWorkerCrash(t *testing.T) {
	p := newTestPool(t, Config{Workers: 1, RestartBackoff: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	before, err := request(t, p, "")
	if err != nil {
		t.Fatalf("çökme öncesi istek: %v", err)
	}

	// Yanıt üretmeden ölen bir istek: hata dönmeli, havuz kilitlenmemeli.
	if _, err := request(t, p, "crash=1"); err == nil {
		t.Error("süreç yanıtsız öldüğü hâlde istek başarılı sayıldı")
	}

	// Havuz kendini toparlamalı.
	deadline := time.Now().Add(15 * time.Second)
	var after string
	for time.Now().Before(deadline) {
		after, err = request(t, p, "")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("çökme sonrası havuz toparlanamadı: %v", err)
	}
	if pidOf(t, before) == pidOf(t, after) {
		t.Error("çöken süreç yeniden başlatılmamış: pid değişmedi")
	}
	if got := p.Stats().Crashed; got < 1 {
		t.Errorf("çökme sayacı %d, beklenen en az 1", got)
	}
}

func TestPoolRestartsWorkerKilledFromOutside(t *testing.T) {
	// Boştaki bir işçinin süreci haber vermeden giderse (ölümcül PHP hatası,
	// görev yöneticisinden kapatma, OOM) denetçi bunu fark edip yerine
	// yenisini koymalı.
	p := newTestPool(t, Config{Workers: 1, RestartBackoff: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	body, err := request(t, p, "")
	if err != nil {
		t.Fatalf("ilk istek: %v", err)
	}
	first := pidOf(t, body)

	if err := killProcess(first); err != nil {
		t.Fatalf("pid %d öldürülemedi: %v", first, err)
	}

	var second int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		body, err := request(t, p, "")
		if err == nil {
			if pid := pidOf(t, body); pid != first {
				second = pid
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if second == 0 {
		t.Fatalf("öldürülen işçi (pid %d) yerine yenisi başlatılmadı; sayaçlar: %+v", first, p.Stats())
	}
	if got := p.Stats().Crashed; got < 1 {
		t.Errorf("çökme sayacı %d, beklenen en az 1", got)
	}
}

func TestPoolAcquireHonoursContextDeadline(t *testing.T) {
	p := newTestPool(t, Config{Workers: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}

	// Tek işçiyi uzun bir istekle meşgul et.
	busy := make(chan struct{})
	go func() {
		defer close(busy)
		request(t, p, "sleep=2s")
	}()
	waitForIdle(t, p, 0)

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer shortCancel()

	start := time.Now()
	_, err := p.Do(shortCtx, params("/index.php", ""), nil)
	if err == nil {
		t.Fatal("boş işçi yokken istek hata vermedi")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("bağlam süresi dolduğu hâlde %v beklendi", elapsed)
	}
	<-busy
}

func TestPoolCloseLeavesNoProcesses(t *testing.T) {
	p := newTestPool(t, Config{Workers: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("havuz hazır olmadı: %v", err)
	}
	waitForIdle(t, p, 3)

	pids := map[int]bool{}
	for i := 0; i < 6; i++ {
		body, err := request(t, p, "")
		if err != nil {
			t.Fatalf("istek: %v", err)
		}
		pids[pidOf(t, body)] = true
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("ikinci Close: %v", err)
	}

	for pid := range pids {
		if processAlive(pid) {
			t.Errorf("pid %d Close sonrası hâlâ ayakta", pid)
		}
	}

	if _, err := request(t, p, ""); err != ErrClosed {
		t.Errorf("kapalı havuzda hata = %v, beklenen ErrClosed", err)
	}
}

func TestPoolReadyFailsWhenExecutableIsBroken(t *testing.T) {
	p := newTestPool(t, Config{
		Workers:        1,
		SpawnTimeout:   time.Second,
		RestartBackoff: 20 * time.Millisecond,
		Env:            []string{"FAKE_STARTUP_FAIL=1"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := p.Ready(ctx); err == nil {
		t.Fatal("süreç hiç ayağa kalkmadığı hâlde havuz hazır sayıldı")
	}
}

func TestPoolStartsSlowWorker(t *testing.T) {
	p := newTestPool(t, Config{
		Workers:      1,
		SpawnTimeout: 10 * time.Second,
		Env:          []string{"FAKE_STARTUP_DELAY=400ms"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("yavaş açılan işçi beklenmedi: %v", err)
	}
	if _, err := request(t, p, ""); err != nil {
		t.Fatalf("istek: %v", err)
	}
}

func waitForIdle(t *testing.T, p *Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if p.Stats().Idle == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("boştaki işçi sayısı %d, beklenen %d", p.Stats().Idle, want)
}
