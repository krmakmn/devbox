package phppool

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// worker, tek bir php-cgi sürecini ve onun yaşam döngüsünü temsil eder.
//
// php-cgi aynı anda tek istek işler; eşzamanlılık bu yüzden süreç sayısıyla
// sağlanır. Bir işçi ya boştadır (havuzda), ya bir isteğe ayrılmıştır, ya da
// ölüdür ve yeniden doğmayı bekler.
type worker struct {
	id   int
	pool *Pool

	// Aşağıdakiler pool.mu altında korunur.
	ready    bool
	addr     string
	requests int

	// fixedPort, havuz kurulurken tahsis edilmiş sabit port. 0 ise her
	// başlatmada işletim sisteminden yeni port isteniyor.
	fixedPort int

	// Yalnız run goroutine'i tarafından kullanılır.
	cmd      *exec.Cmd
	stderr   *ringBuffer
	recycleC chan struct{}

	recycleOnce sync.Mutex
}

func newWorker(id int, p *Pool) *worker {
	return &worker{
		id:       id,
		pool:     p,
		recycleC: make(chan struct{}, 1),
	}
}

// run, işçinin ömür boyu süren denetim döngüsüdür: süreci başlatır, hazır
// olunca havuza sokar, ölünce geri çeker ve yeniden başlatır.
func (w *worker) run(ctx context.Context) {
	defer w.pool.wg.Done()

	backoff := w.pool.cfg.RestartBackoff
	for ctx.Err() == nil {
		startedAt := time.Now()

		if err := w.spawn(ctx); err != nil {
			w.pool.logf("işçi %d başlatılamadı: %v", w.id, err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, w.pool.cfg.MaxRestartBackoff)
			continue
		}

		w.pool.markUp(w)
		w.pool.logf("işçi %d hazır (%s, pid %d)", w.id, w.addr, w.cmd.Process.Pid)

		exited := make(chan error, 1)
		go func(cmd *exec.Cmd) { exited <- cmd.Wait() }(w.cmd)

		var exitErr error
		select {
		case <-ctx.Done():
			w.pool.markDown(w)
			w.kill()
			<-exited
			return
		case <-w.recycleC:
			w.pool.markDown(w)
			// Yenileme iki sebeple istenmiş olabilir: işçi istek kotasını
			// doldurdu (sağlıklı) ya da bir istek sırasında süreç öldü ve
			// bağlantı koptu. İkisini ayırmak için sürece kısa bir mühlet
			// tanıyoruz: zaten ölmüşse Wait hemen döner ve bunu çökme olarak
			// sayarız; ayaktaysa biz öldürürüz.
			select {
			case exitErr = <-exited:
				w.pool.stats.crashed.Add(1)
				w.pool.logf("işçi %d istek sırasında sonlandı: %v%s",
					w.id, exitErr, w.stderrSuffix())
			case <-time.After(50 * time.Millisecond):
				w.kill()
				<-exited
				w.pool.stats.recycled.Add(1)
			}
		case exitErr = <-exited:
			w.pool.markDown(w)
			w.pool.stats.crashed.Add(1)
			w.pool.logf("işçi %d beklenmedik biçimde sonlandı: %v%s",
				w.id, exitErr, w.stderrSuffix())
		}

		// Süreç uzun süre ayakta kaldıysa geri çekilme sayacını sıfırla:
		// tek seferlik bir çökme, kalıcı bir hatayla aynı muameleyi görmemeli.
		if time.Since(startedAt) > w.pool.cfg.HealthyUptime {
			backoff = w.pool.cfg.RestartBackoff
		} else {
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, w.pool.cfg.MaxRestartBackoff)
		}
	}
}

// spawn, süreci başlatır ve dinlemeye başlayana kadar bekler.
func (w *worker) spawn(ctx context.Context) error {
	addr := w.assignedAddr()
	if addr == "" {
		var err error
		addr, err = reserveLoopbackPort(w.pool.cfg.Host)
		if err != nil {
			return fmt.Errorf("boş port bulunamadı: %w", err)
		}
	}

	args := append([]string{"-b", addr}, w.pool.cfg.Args...)
	cmd := exec.Command(w.pool.cfg.Exec, args...)
	cmd.Dir = w.pool.cfg.WorkDir
	cmd.Env = w.pool.childEnv()

	// php-cgi'nin başlangıç hataları ("Failed to listen", eksik uzantı,
	// bozuk php.ini) yalnız burada görünür; halka tamponunda tutup çökme
	// günlüğüne ekliyoruz.
	w.stderr = newRingBuffer(8 * 1024)
	cmd.Stderr = w.stderr
	cmd.Stdout = w.stderr

	w.pool.group.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s çalıştırılamadı: %w", w.pool.cfg.Exec, err)
	}
	if err := w.pool.group.Add(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}

	w.cmd = cmd
	w.addr = addr

	if err := waitForListener(ctx, addr, w.pool.cfg.SpawnTimeout); err != nil {
		w.kill()
		cmd.Wait()
		return fmt.Errorf("işçi %s adresinde dinlemeye başlamadı: %w%s", addr, err, w.stderrSuffix())
	}
	return nil
}

// assignedAddr, sabit port verilmişse adresi döner.
func (w *worker) assignedAddr() string {
	if w.fixedPort == 0 {
		return ""
	}
	return net.JoinHostPort(w.pool.cfg.Host, strconv.Itoa(w.fixedPort))
}

// requestRecycle, işçinin ilk fırsatta yenilenmesini ister. Çağrı
// bloklamaz; aynı anda birden çok kez çağrılması güvenlidir.
func (w *worker) requestRecycle() {
	select {
	case w.recycleC <- struct{}{}:
	default:
	}
}

func (w *worker) kill() {
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
}

func (w *worker) stderrSuffix() string {
	if w.stderr == nil {
		return ""
	}
	out := bytes.TrimSpace(w.stderr.Bytes())
	if len(out) == 0 {
		return ""
	}
	return "\n--- süreç çıktısı ---\n" + string(out)
}

// waitForListener, adres bağlantı kabul edene kadar kısa aralıklarla dener.
func waitForListener(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("%s içinde hazır olmadı: %w", timeout, lastErr)
		}
		if !sleepCtx(ctx, 20*time.Millisecond) {
			return ctx.Err()
		}
	}
}

// reserveLoopbackPort, işletim sisteminden boş bir port ister.
//
// Portu seçtirip hemen bırakıyoruz; çocuk süreç onu bağlayana kadar küçük bir
// yarış penceresi kalıyor ama bunun karşılığında Windows'ta Hyper-V'nin
// rezerve ettiği aralıklara denk gelme sorunu tamamen ortadan kalkıyor —
// çekirdek zaten yalnız gerçekten kullanılabilir bir port veriyor.
func reserveLoopbackPort(host string) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// ringBuffer, son N baytı tutan basit bir tampondur: çok konuşan bir süreç
// belleği doldurmasın diye.
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRingBuffer(limit int) *ringBuffer { return &ringBuffer{limit: limit} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = r.buf[len(r.buf)-r.limit:]
	}
	return len(p), nil
}

func (r *ringBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf...)
}
