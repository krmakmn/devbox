package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/proc"
)

// withLogs, günlük tamponuna ihtiyaç duyan hazır olma ölçütlerinin
// uyguladığı arayüz. LogReady'yi kullanıcının tampona erişmeden
// kurabilmesi için var.
type withLogs interface {
	withLogs(*LogBuffer) ReadyCheck
}

func (l LogReady) withLogs(b *LogBuffer) ReadyCheck {
	l.logs = b
	return l
}

// Supervisor, bir grup servisi yönetir.
type Supervisor struct {
	group  *proc.Group
	logger *slog.Logger

	mu       sync.Mutex
	services map[string]*Service
	order    []string
	closed   bool
}

// New, süreç grubunu kurar.
func New(logger *slog.Logger) (*Supervisor, error) {
	group, err := proc.NewGroup()
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		group:    group,
		logger:   logger,
		services: make(map[string]*Service),
	}, nil
}

// Add, yeni bir servis tanımlar. Başlatmaz.
func (s *Supervisor) Add(cfg Config) (*Service, error) {
	if cfg.Name == "" {
		return nil, errors.New("supervisor: servis adı boş olamaz")
	}
	if cfg.Exec == "" {
		return nil, fmt.Errorf("supervisor: %s için çalıştırılabilir belirtilmemiş", cfg.Name)
	}
	cfg.applyDefaults()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("supervisor: kapatılmış")
	}
	if _, exists := s.services[cfg.Name]; exists {
		return nil, fmt.Errorf("supervisor: %s adında bir servis zaten var", cfg.Name)
	}

	logs := NewLogBuffer(cfg.LogLimit)
	if needs, ok := cfg.Ready.(withLogs); ok {
		cfg.Ready = needs.withLogs(logs)
	}

	svc := &Service{
		cfg:    cfg,
		group:  s.group,
		logs:   logs,
		logger: s.logger,
		state:  StateStopped,
		since:  time.Now(),
	}
	s.services[cfg.Name] = svc
	s.order = append(s.order, cfg.Name)
	return svc, nil
}

// Get, adı verilen servisi döner.
func (s *Supervisor) Get(name string) (*Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.services[name]
	return svc, ok
}

// Status, tüm servislerin durumunu ekleme sırasına göre döner.
func (s *Supervisor) Status() []Status {
	s.mu.Lock()
	names := append([]string(nil), s.order...)
	services := make([]*Service, 0, len(names))
	for _, n := range names {
		services = append(services, s.services[n])
	}
	s.mu.Unlock()

	out := make([]Status, 0, len(services))
	for _, svc := range services {
		out = append(out, svc.Status())
	}
	return out
}

// StartAll, tüm servisleri sırayla başlatır. İlk hatada durur.
func (s *Supervisor) StartAll(ctx context.Context) error {
	s.mu.Lock()
	names := append([]string(nil), s.order...)
	s.mu.Unlock()

	for _, name := range names {
		svc, ok := s.Get(name)
		if !ok {
			continue
		}
		if err := svc.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close, tüm servisleri durdurur ve süreç grubunu kapatır.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	services := make([]*Service, 0, len(s.services))
	for _, svc := range s.services {
		services = append(services, svc)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			svc.Stop()
		}(svc)
	}
	wg.Wait()

	// İş nesnesi son savunma hattı: yukarıdaki durdurma bir sebeple
	// başarısız olduysa bile çekirdek kalanları öldürür.
	return s.group.Close()
}

// Service, tek bir yönetilen süreç.
type Service struct {
	cfg    Config
	group  *proc.Group
	logs   *LogBuffer
	logger *slog.Logger

	mu       sync.Mutex
	state    State
	pid      int
	since    time.Time
	restarts int
	lastErr  error
	cmd      *exec.Cmd

	runMu   sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}
}

// Logs, servisin günlük tamponu.
func (s *Service) Logs() *LogBuffer { return s.logs }

// Status, servisin o anki durumu.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		Name:     s.cfg.Name,
		State:    s.state.String(),
		PID:      s.pid,
		Since:    s.since,
		Restarts: s.restarts,
		Ready:    s.cfg.Ready.Describe(),
	}
	if s.lastErr != nil {
		st.LastErr = s.lastErr.Error()
	}
	return st
}

// State, servisin durumu.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Start, servisi başlatır ve hazır olana kadar bekler.
//
// Hazır olmayı beklemek isteğe bağlı bir incelik değil: port dinlemeye
// başlamamış bir MySQL'e bağlanmaya çalışmak, kullanıcının göreceği ilk
// hatanın gerçek sebeple ilgisiz olması demek.
func (s *Service) Start(ctx context.Context) error {
	s.runMu.Lock()
	if s.stopped != nil {
		s.runMu.Unlock()
		return nil // zaten çalışıyor
	}

	runCtx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	readyCh := make(chan error, 1)
	s.cancel, s.stopped = cancel, stopped
	// Kanalları alan olarak değil parametre olarak veriyoruz: Stop
	// alanları nil'liyor ve run'ın defer'i alandan okusaydı kanalı hiç
	// kapatmazdı — Stop da onu beklerken sonsuza kadar asılı kalırdı.
	go s.run(runCtx, stopped, readyCh)
	s.runMu.Unlock()

	select {
	case err := <-readyCh:
		if err != nil {
			s.Stop()
			return err
		}
		return nil
	case <-ctx.Done():
		s.Stop()
		return fmt.Errorf("supervisor: %s başlatılırken beklenmedi: %w", s.cfg.Name, ctx.Err())
	}
}

// Stop, servisi durdurur ve denetim döngüsünü sonlandırır.
func (s *Service) Stop() {
	s.runMu.Lock()
	cancel, stopped := s.cancel, s.stopped
	s.cancel, s.stopped = nil, nil
	s.runMu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-stopped
}

// run, servisin ömür boyu süren denetim döngüsü.
func (s *Service) run(ctx context.Context, stopped chan struct{}, ready chan error) {
	defer close(stopped)

	backoff := s.cfg.RestartBackoff
	attempts := 0
	firstStart := true

	for ctx.Err() == nil {
		startedAt := time.Now()
		s.setState(StateStarting, 0, nil)

		cmd, err := s.spawn(ctx)
		if err != nil {
			s.fail(ready, err, firstStart)
			if !s.shouldRetry(&attempts) {
				s.setState(StateFailed, 0, err)
				return
			}
			if !sleepCtx(ctx, backoff) {
				s.setState(StateStopped, 0, nil)
				return
			}
			backoff = nextBackoff(backoff, s.cfg.MaxRestartBackoff)
			firstStart = false
			continue
		}

		exited := make(chan error, 1)
		go func(c *exec.Cmd) { exited <- c.Wait() }(cmd)

		alreadyExited, err := s.waitReady(ctx, exited)
		if err != nil {
			// Süreç kendiliğinden öldüyse öldürüp beklemeye kalkmıyoruz:
			// çıkış kanalı zaten tüketildiği için killAndWait boşuna
			// StopTimeout kadar oturur.
			if !alreadyExited {
				s.killAndWait(cmd, exited)
			}
			s.fail(ready, err, firstStart)
			if !s.shouldRetry(&attempts) {
				s.setState(StateFailed, 0, err)
				return
			}
			if !sleepCtx(ctx, backoff) {
				s.setState(StateStopped, 0, nil)
				return
			}
			backoff = nextBackoff(backoff, s.cfg.MaxRestartBackoff)
			firstStart = false
			continue
		}

		s.setState(StateRunning, cmd.Process.Pid, nil)
		if firstStart {
			signalReady(ready, nil)
			firstStart = false
		}
		s.logf("servis hazır", "servis", s.cfg.Name, "pid", cmd.Process.Pid)

		select {
		case <-ctx.Done():
			s.shutdown(cmd, exited)
			s.setState(StateStopped, 0, nil)
			return
		case exitErr := <-exited:
			s.mu.Lock()
			s.restarts++
			s.mu.Unlock()
			s.setState(StateBackoff, 0, exitErr)
			s.logf("servis sonlandı", "servis", s.cfg.Name, "hata", exitErr)

			if s.cfg.Restart == RestartNever {
				s.setState(StateStopped, 0, exitErr)
				return
			}
			// Uzun süre ayakta kaldıysa bu tek seferlik bir çökme sayılır.
			if time.Since(startedAt) > s.cfg.HealthyUptime {
				backoff = s.cfg.RestartBackoff
				attempts = 0
			} else if !s.shouldRetry(&attempts) {
				s.setState(StateFailed, 0, exitErr)
				return
			}
			if !sleepCtx(ctx, backoff) {
				s.setState(StateStopped, 0, nil)
				return
			}
			backoff = nextBackoff(backoff, s.cfg.MaxRestartBackoff)
		}
	}
	s.setState(StateStopped, 0, nil)
}

func (s *Service) spawn(ctx context.Context) (*exec.Cmd, error) {
	cmd := exec.Command(s.cfg.Exec, s.cfg.Args...)
	cmd.Dir = s.cfg.WorkDir
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	cmd.Stdout = s.logs
	cmd.Stderr = s.logs

	// Hazır olma ölçütü yalnız bu koşunun çıktısına baksın.
	s.logs.MarkStart()

	// Halka tamponu yeniden başlatmalar boyunca yaşıyor; ayraç olmadan
	// iki koşunun çıktısı birbirine yapışıyor ve "hangi 'hazır' satırı
	// şimdikine ait?" sorusu cevapsız kalıyor.
	fmt.Fprintf(s.logs, "──── %s başlatılıyor · %s ────\n",
		s.cfg.Name, time.Now().Format("15:04:05"))

	s.group.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("supervisor: %s başlatılamadı: %w", s.cfg.Name, err)
	}
	if err := s.group.Add(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	return cmd, nil
}

// waitReady, hazır olma ölçütünü sağlanana kadar yoklar.
//
// Süreç bu sırada ölürse beklemeyi kesiyoruz: ölmüş bir sürecin hazır
// olmasını zaman aşımına kadar beklemek, kullanıcıyı 30 saniye boşuna
// oyalamak demek.
// Dönen ilk değer, sürecin beklerken kendiliğinden ölüp ölmediğini söyler;
// çağıran buna göre öldürme adımını atlar.
func (s *Service) waitReady(ctx context.Context, exited <-chan error) (bool, error) {
	deadline := time.Now().Add(s.cfg.StartTimeout)
	var lastErr error

	for {
		select {
		case exitErr := <-exited:
			return true, fmt.Errorf("supervisor: %s hazır olmadan sonlandı: %v%s",
				s.cfg.Name, exitErr, s.logTail())
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.cfg.Ready.Ready(checkCtx)
		cancel()
		if err == nil {
			return false, nil
		}
		lastErr = err

		if time.Now().After(deadline) {
			return false, fmt.Errorf("supervisor: %s %s içinde hazır olmadı (%s): %v%s",
				s.cfg.Name, s.cfg.StartTimeout, s.cfg.Ready.Describe(), lastErr, s.logTail())
		}
		if !sleepCtx(ctx, 50*time.Millisecond) {
			return false, ctx.Err()
		}
	}
}

// shutdown, süreci nazikçe durdurmayı dener, olmazsa öldürür.
//
// Windows'ta taşınabilir bir "nazik dur" sinyali yok: os.Interrupt
// gönderilemiyor. Bu yüzden veritabanları gibi düzgün kapanması gereken
// servislerin kendi kapatma komutlarını (mysqladmin shutdown, pg_ctl stop)
// tanımlaması gerekecek; genel denetçinin yapabileceği tek şey öldürmek.
func (s *Service) shutdown(cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process == nil {
		return
	}
	// Sinyal sürecin kendisine değil, ağacına gidiyor. Yapılandırmadaki
	// komutlar çoğu zaman sarmalayıcı ("sh -c ...", "npm run dev"); yalnız
	// sarmalayıcıya sinyal göndermek asıl işi yapan torunu ayakta
	// bırakıyor ve o boruları açık tuttuğu için cmd.Wait() dönmüyor.
	if err := proc.TerminateTree(cmd); err == nil {
		select {
		case <-exited:
			return
		case <-time.After(s.cfg.StopTimeout):
		}
	}
	s.killAndWait(cmd, exited)
}

func (s *Service) killAndWait(cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process != nil {
		proc.KillTree(cmd)
	}
	select {
	case <-exited:
	case <-time.After(s.cfg.StopTimeout):
	}
}

func (s *Service) shouldRetry(attempts *int) bool {
	if s.cfg.Restart == RestartNever {
		return false
	}
	*attempts++
	return s.cfg.MaxRestarts == 0 || *attempts < s.cfg.MaxRestarts
}

// fail, başlatma hatasını kaydeder ve ilk denemeyse Start'ı uyandırır.
func (s *Service) fail(ready chan error, err error, firstStart bool) {
	s.logf("servis başlatılamadı", "servis", s.cfg.Name, "hata", err)
	if firstStart {
		signalReady(ready, err)
	}
}

// signalReady, Start'ı bekleten kanalı uyandırır. Tampon 1 olduğu için
// bloklamıyor; ikinci bir bildirim sessizce düşüyor.
func signalReady(ready chan error, err error) {
	select {
	case ready <- err:
	default:
	}
}

func (s *Service) setState(st State, pid int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != st {
		s.since = time.Now()
	}
	s.state = st
	s.pid = pid
	if err != nil {
		s.lastErr = err
	}
}

// logTail, çökme mesajına eklenecek son çıktı.
func (s *Service) logTail() string {
	out := s.logs.String()
	if out == "" {
		return ""
	}
	const max = 2000
	if len(out) > max {
		out = "…" + out[len(out)-max:]
	}
	return "\n--- son çıktı ---\n" + out
}

func (s *Service) logf(msg string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Info(msg, args...)
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
