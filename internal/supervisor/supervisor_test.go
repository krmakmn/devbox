package supervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("DEVBOX_FAKE_SERVICE") == "1" {
		runFakeService()
		return
	}
	os.Exit(m.Run())
}

func newSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeConfig, sahte servisi çalıştıran bir yapılandırma üretir.
func fakeConfig(name string, env ...string) Config {
	return Config{
		Name:           name,
		Exec:           os.Args[0],
		Env:            append([]string{"DEVBOX_FAKE_SERVICE=1"}, env...),
		StartTimeout:   15 * time.Second,
		RestartBackoff: 20 * time.Millisecond,
	}
}

func TestStartsAndReportsRunning(t *testing.T) {
	sup := newSupervisor(t)
	portFile := filepath.Join(t.TempDir(), "port")

	cfg := fakeConfig("web", "FAKE_LISTEN=127.0.0.1:0", "FAKE_PORT_FILE="+portFile)
	cfg.Ready = ImmediateReady{}
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st := svc.Status()
	if st.State != StateRunning.String() {
		t.Errorf("durum %q, beklenen %q", st.State, StateRunning.String())
	}
	if st.PID == 0 {
		t.Error("pid bildirilmedi")
	}

	svc.Stop()
	if got := svc.State(); got != StateStopped {
		t.Errorf("durdurmadan sonra durum %v", got)
	}
}

// Hazır olmayı beklemek isteğe bağlı bir incelik değil: port dinlemeye
// başlamamış bir servise bağlanmak, kullanıcının göreceği ilk hatanın
// gerçek sebeple ilgisiz olması demek.
func TestWaitsForTCPReadiness(t *testing.T) {
	sup := newSupervisor(t)
	addr := freeAddr(t)

	cfg := fakeConfig("db", "FAKE_LISTEN="+addr, "FAKE_STARTUP_DELAY=600ms")
	cfg.Ready = TCPReady{Addr: addr}
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Errorf("Start %v sonra döndü; hazır olmayı beklemiyor", elapsed)
	}
	// Start döndüğünde servis gerçekten bağlantı kabul etmeli.
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("Start döndükten sonra bağlanılamadı: %v", err)
	}
	resp.Body.Close()
}

func TestWaitsForLogReadiness(t *testing.T) {
	sup := newSupervisor(t)

	cfg := fakeConfig("pg", "FAKE_STARTUP_DELAY=400ms",
		"FAKE_LOG=database system is ready to accept connections")
	cfg.Ready = LogReady{Substring: "ready to accept connections"}
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("Start %v sonra döndü; günlük satırını beklemiyor", elapsed)
	}
	if !strings.Contains(svc.Logs().String(), "ready to accept") {
		t.Error("günlük tamponunda beklenen satır yok")
	}
}

// Ölmüş bir sürecin hazır olmasını zaman aşımına kadar beklemek,
// kullanıcıyı 30 saniye boşuna oyalamak demek.
func TestFailsFastWhenProcessDiesBeforeReady(t *testing.T) {
	sup := newSupervisor(t)
	addr := freeAddr(t)

	cfg := fakeConfig("olu", "FAKE_EXIT_CODE=1")
	cfg.Ready = TCPReady{Addr: addr}
	cfg.StartTimeout = 20 * time.Second
	cfg.MaxRestarts = 1
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	err = svc.Start(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("süreç hemen öldüğü hâlde Start başarılı döndü")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Start %v bekledi; süreç öldüğünde beklemeyi kesmiyor", elapsed)
	}
	// Hata, sebebi görmek için sürecin çıktısını taşımalı.
	if !strings.Contains(err.Error(), "başlatma hatası") {
		t.Errorf("hata sürecin çıktısını içermiyor: %v", err)
	}
}

func TestRestartsCrashedService(t *testing.T) {
	sup := newSupervisor(t)

	// 700 ms sonra kendiliğinden çıkan bir servis.
	cfg := fakeConfig("kirilgan", "FAKE_LIVE_FOR=700ms")
	cfg.HealthyUptime = 10 * time.Second
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Status().Restarts >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("yeniden başlatma sayacı %d, beklenen en az 2", svc.Status().Restarts)
}

func TestGivesUpAfterMaxRestarts(t *testing.T) {
	// Sürekli çöken bir süreç makineyi yakmamalı.
	sup := newSupervisor(t)

	cfg := fakeConfig("umutsuz", "FAKE_EXIT_CODE=2")
	cfg.MaxRestarts = 3
	cfg.RestartBackoff = 10 * time.Millisecond
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// ImmediateReady "çatallandıysa başlamıştır" demek (systemd
	// Type=simple gibi), dolayısıyla Start'ın burada başarı dönmesi
	// beklenen davranış; asıl sınadığımız, çöküş döngüsünün sonsuza kadar
	// sürmemesi.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = svc.Start(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.State() == StateFailed || svc.State() == StateStopped {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("son durum %v; vazgeçmesi bekleniyordu", svc.State())
}

func TestRestartNeverKeepsServiceStopped(t *testing.T) {
	sup := newSupervisor(t)
	cfg := fakeConfig("tekseferlik", "FAKE_LIVE_FOR=300ms")
	cfg.Restart = RestartNever
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1500 * time.Millisecond)
	if got := svc.Status().Restarts; got > 1 {
		t.Errorf("RestartNever'a rağmen %d kez yeniden başlatıldı", got)
	}
}

func TestCloseStopsEverything(t *testing.T) {
	sup, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	var pids []int
	for i := 0; i < 3; i++ {
		svc, err := sup.Add(fakeConfig(fmt.Sprintf("servis%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := svc.Start(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		pids = append(pids, svc.Status().PID)
	}

	if err := sup.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sup.Close(); err != nil {
		t.Fatalf("ikinci Close: %v", err)
	}

	for _, pid := range pids {
		if processAlive(pid) {
			t.Errorf("pid %d Close sonrası hâlâ ayakta", pid)
		}
	}
}

func TestDuplicateNameRejected(t *testing.T) {
	sup := newSupervisor(t)
	if _, err := sup.Add(fakeConfig("aynı")); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Add(fakeConfig("aynı")); err == nil {
		t.Error("aynı adla ikinci servis kabul edildi")
	}
	if _, err := sup.Add(Config{Name: "eksik"}); err == nil {
		t.Error("çalıştırılabilir belirtilmeden servis kabul edildi")
	}
}

func TestStatusListIsOrdered(t *testing.T) {
	sup := newSupervisor(t)
	for _, name := range []string{"once", "sonra", "en-son"} {
		if _, err := sup.Add(fakeConfig(name)); err != nil {
			t.Fatal(err)
		}
	}
	status := sup.Status()
	if len(status) != 3 {
		t.Fatalf("%d durum döndü", len(status))
	}
	// Ekleme sırası korunmalı: kullanıcı servisleri bağımlılık sırasına
	// göre tanımlıyor, listede karışması kafa karıştırıcı.
	if status[0].Name != "once" || status[2].Name != "en-son" {
		t.Errorf("sıra korunmadı: %v", []string{status[0].Name, status[1].Name, status[2].Name})
	}
}

// --- günlük tamponu -------------------------------------------------------

func TestLogBufferKeepsLastBytes(t *testing.T) {
	b := NewLogBuffer(100)
	for i := 0; i < 50; i++ {
		fmt.Fprintf(b, "satır-%02d\n", i)
	}
	if got := len(b.Bytes()); got > 100 {
		t.Errorf("tampon %d bayt, sınır 100", got)
	}
	// Son satırlar korunmalı; ilkler değil.
	if !strings.Contains(b.String(), "satır-49") {
		t.Error("son satır tamponda yok")
	}
	if strings.Contains(b.String(), "satır-00") {
		t.Error("sınır aşıldığı hâlde ilk satır duruyor")
	}
}

func TestLogBufferSubscribers(t *testing.T) {
	b := NewLogBuffer(1024)
	ch, unsubscribe := b.Subscribe()

	go io.WriteString(b, "merhaba\n")

	select {
	case line := <-ch:
		if string(line) != "merhaba\n" {
			t.Errorf("satır = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("abone satırı almadı")
	}

	unsubscribe()
	if _, ok := <-ch; ok {
		t.Error("abonelik sonlandıktan sonra kanal kapanmadı")
	}
}

// Yavaş bir abone yüzünden sürecin çıktısı tıkanmamalı.
func TestSlowSubscriberDoesNotBlockWrites(t *testing.T) {
	b := NewLogBuffer(1 << 20)
	_, unsubscribe := b.Subscribe() // hiç okumayan abone
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(b, "satır %d\n", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("yavaş abone yazmayı kilitledi")
	}
}

func TestLogBufferConcurrentWriters(t *testing.T) {
	b := NewLogBuffer(1 << 16)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				fmt.Fprintf(b, "%d-%d\n", i, j)
			}
		}(i)
	}
	wg.Wait()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// Gerçek bir hatanın testi: günlük tamponu yeniden başlatmalar arasında
// korunuyor. LogReady tamponun tamamına bakarsa, önceki koşudan kalan
// "hazır" satırı yeni süreci hazır sayar ve Start, süreç henüz portu
// açmamışken döner. Veritabanı örneklerinde bu, anlık görüntüden sonraki
// ilk bağlantının reddedilmesi olarak ortaya çıkmıştı.
func TestLogReadyIgnoresPreviousRunOutput(t *testing.T) {
	sup := newSupervisor(t)

	cfg := fakeConfig("gecikmeli", "FAKE_STARTUP_DELAY=500ms", "FAKE_LOG=HAZIR")
	cfg.Ready = LogReady{Substring: "HAZIR"}
	cfg.RestartBackoff = 10 * time.Millisecond
	svc, err := sup.Add(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	firstPID := svc.Status().PID

	// Süreci dışarıdan öldür: denetçi yenisini başlatacak ve tamponda
	// eski koşunun "HAZIR" satırı duracak.
	if err := killProcess(firstPID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var secondPID int
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.State == StateRunning.String() && st.PID != 0 && st.PID != firstPID {
			secondPID = st.PID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if secondPID == 0 {
		t.Fatalf("süreç yeniden başlamadı; durum: %+v", svc.Status())
	}

	// Yeni süreç 500 ms gecikmeyle yazıyor. Durum "çalışıyor" olduysa,
	// ölçüt yeni koşunun satırını görmüş olmalı — eskisini değil.
	if !strings.Contains(svc.Logs().SinceStart(), "HAZIR") {
		t.Error("hazır olma ölçütü eski koşunun çıktısıyla geçmiş olabilir")
	}
}

func TestLogBufferSinceStart(t *testing.T) {
	b := NewLogBuffer(1024)
	io.WriteString(b, "birinci koşu: HAZIR\n")

	if !strings.Contains(b.SinceStart(), "birinci") {
		t.Error("işaretlemeden önceki çıktı görünmüyor")
	}

	b.MarkStart()
	if got := b.SinceStart(); got != "" {
		t.Errorf("işaretlemeden hemen sonra %q, beklenen boş", got)
	}

	io.WriteString(b, "ikinci koşu\n")
	since := b.SinceStart()
	if !strings.Contains(since, "ikinci") {
		t.Error("işaretlemeden sonraki çıktı görünmüyor")
	}
	if strings.Contains(since, "birinci") {
		t.Error("işaretlemeden önceki çıktı hâlâ görünüyor")
	}
	// Tamponun tamamı ise her ikisini de tutmalı; çökme tanısı için lazım.
	if !strings.Contains(b.String(), "birinci") {
		t.Error("tampon önceki koşuyu atmış; çökme tanısı kaybolur")
	}
}

// Sarmalayıcı komutlar (sh -c, npm run dev) asıl işi bir torun sürece
// yaptırıyor. Yalnız sarmalayıcıyı durdurmak torunu ayakta bırakıyor; torun
// boruları açık tuttuğu için cmd.Wait() dönmüyor ve kapanış StopTimeout
// kadar (iki kez) sürüyor. Ctrl+C'den sonra 20 saniye bekleyen bir CLI,
// kullanıcı için takılmış demektir.
func TestStopKillsGrandchildrenQuickly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kabuk komutu Windows'ta farklı")
	}
	sup, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	svc, err := sup.Add(Config{
		Name: "sarmalayıcı",
		Exec: "/bin/sh",
		Args: []string{"-c", "echo hazır; exec sleep 120 & sleep 120"},
		// Torun ölmezse iki kez bu süre kadar beklenirdi.
		StopTimeout: 3 * time.Second,
		Ready:       LogReady{Substring: "hazır"},
		Restart:     RestartNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	svc.Stop()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("durdurma %v sürdü; torun süreç ağacı öldürülmemiş olabilir", elapsed)
	}
}
