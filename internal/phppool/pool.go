// Package phppool, php-cgi süreçlerinden oluşan bir FastCGI havuzu yönetir.
//
// Neden var: Windows'ta PHP-FPM yok. Elde tek istek görüp kapanan php-cgi.exe
// var; onu kalıcı FastCGI kipinde (-b adres) çalıştırıp süreç yönetimini
// kendimiz üstleniyoruz. Havuzun sorumlulukları:
//
//   - N adet süreci ayakta tutmak, ölenin yerine yenisini koymak
//     (üstel geri çekilmeyle, çökme döngüsünde makineyi yakmadan),
//   - istekleri boştaki bir işçiye dağıtmak,
//   - belirli sayıda istekten sonra işçiyi yenilemek (PHP uzantılarındaki
//     bellek sızıntıları uzun ömürlü süreçlerde birikir),
//   - kapanışta hiçbir süreç arkada bırakmamak.
//
// Havuz proje başınadır: her projenin kendi PHP sürümü, kendi php.ini katmanı
// ve kendi Xdebug ayarı olabilsin diye.
package phppool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/krmakmn/devbox/internal/fastcgi"
	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/proc"
)

// Config, bir havuzun ayarlarıdır. Sıfır değerler makul varsayılanlara
// çekilir; yalnız Exec zorunludur.
type Config struct {
	// Name, günlüklerde görünen havuz adı (genelde proje adı).
	Name string

	// Exec, php-cgi çalıştırılabilirinin yolu (Windows'ta php-cgi.exe).
	Exec string

	// Args, -b adres'ten sonra eklenecek ek argümanlar
	// (ör. -c ile projeye özel php.ini dizini).
	Args []string

	// Env, çocuk sürece eklenecek ortam değişkenleri ("K=V" biçiminde).
	Env []string

	// WorkDir, çocuk sürecin çalışma dizini.
	WorkDir string

	// Workers, ayakta tutulacak süreç sayısı. 0 ise CPU sayısı kullanılır.
	Workers int

	// MaxRequests, bir işçi kaç istekten sonra yenilenir. 0 = sınırsız.
	MaxRequests int

	// SpawnTimeout, sürecin dinlemeye başlaması için tanınan süre.
	SpawnTimeout time.Duration

	// DialTimeout, hazır bir işçiye bağlanma süresi.
	DialTimeout time.Duration

	// RestartBackoff / MaxRestartBackoff, çökme sonrası bekleme aralığı.
	RestartBackoff    time.Duration
	MaxRestartBackoff time.Duration

	// HealthyUptime, bir sürecin "sağlıklı çalıştı" sayılacağı süre. Bu
	// süreyi aşan bir süreç öldüğünde geri çekilme sayacı sıfırlanır.
	HealthyUptime time.Duration

	// BasePort, işçilere sabit port vermek için başlangıç numarası.
	//
	// 0 ise portları işletim sistemi seçer (her açılışta değişir). Sabit
	// port istemenin sebebi, Apache ve Nginx yapılandırmasının FastCGI
	// adreslerini önceden bilmek zorunda olması: her açılışta değişen bir
	// port, yapılandırmayı her açılışta yeniden yazıp sunucuyu yeniden
	// yüklemek demek.
	//
	// Portlar havuz kurulurken bir kez tahsis edilir ve işçi yeniden
	// başlasa da değişmez.
	BasePort int

	// Host, işçilerin dinleyeceği arayüz. Boşsa 127.0.0.1.
	//
	// Loopback dışına çıkarmak, PHP'yi ağa açmak demektir; php-cgi'de kimlik
	// doğrulama yoktur. Bilerek yapılmadıkça değiştirilmemeli.
	Host string

	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.SpawnTimeout <= 0 {
		c.SpawnTimeout = 10 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.RestartBackoff <= 0 {
		c.RestartBackoff = 200 * time.Millisecond
	}
	if c.MaxRestartBackoff <= 0 {
		c.MaxRestartBackoff = 30 * time.Second
	}
	if c.HealthyUptime <= 0 {
		c.HealthyUptime = 10 * time.Second
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Name == "" {
		c.Name = "php"
	}
}

// ErrClosed, kapatılmış bir havuza istek gönderildiğinde döner.
var ErrClosed = errors.New("phppool: havuz kapatıldı")

// Pool, php-cgi işçilerinden oluşan havuzdur.
type Pool struct {
	cfg       Config
	group     *proc.Group
	allocator *ports.Allocator

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	idle    []*worker
	waiters []chan *worker
	workers []*worker
	closed  bool

	stats struct {
		requests atomic.Int64
		failures atomic.Int64
		recycled atomic.Int64
		crashed  atomic.Int64
	}
}

// New, havuzu kurar ve işçileri başlatır. Dönüşte en az bir işçinin hazır
// olması garanti değildir; bunun için Ready kullanın.
func New(cfg Config) (*Pool, error) {
	if cfg.Exec == "" {
		return nil, errors.New("phppool: Exec boş olamaz")
	}
	cfg.applyDefaults()

	group, err := proc.NewGroup()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{cfg: cfg, group: group, ctx: ctx, cancel: cancel,
		allocator: ports.New(cfg.Host)}

	// Sabit port istendiyse hepsini şimdi tahsis ediyoruz: yapılandırma
	// üretimi adresleri havuz çalışmaya başlamadan önce bilmek zorunda.
	var fixed []int
	if cfg.BasePort > 0 {
		if err := p.allocator.LoadExclusions(ctx); err != nil {
			// Rezervasyon listesi okunamadıysa devam ediyoruz; bağlama
			// denemesi zaten doğruyu söylüyor.
			p.logf("işletim sistemi port rezervasyonları okunamadı: %v", err)
		}
		fixed, err = p.allocator.AllocateSeries(cfg.BasePort, cfg.Workers)
		if err != nil {
			cancel()
			group.Close()
			return nil, err
		}
	}

	for i := 0; i < cfg.Workers; i++ {
		w := newWorker(i, p)
		if i < len(fixed) {
			w.fixedPort = fixed[i]
			w.addr = net.JoinHostPort(cfg.Host, strconv.Itoa(fixed[i]))
		}
		p.workers = append(p.workers, w)
		p.wg.Add(1)
		go w.run(ctx)
	}
	return p, nil
}

// Addrs, işçilerin FastCGI adreslerini döner.
//
// Yalnız BasePort verilmişse anlamlı: adresler o zaman sabittir ve web
// sunucusu yapılandırmasına yazılabilir. Aksi hâlde her yeniden başlatmada
// değişirler.
func (p *Pool) Addrs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.workers))
	for _, w := range p.workers {
		if w.addr != "" {
			out = append(out, w.addr)
		}
	}
	return out
}

// FixedPorts, havuzun sabit port kullanıp kullanmadığını söyler.
func (p *Pool) FixedPorts() bool { return p.cfg.BasePort > 0 }

// Ready, en az bir işçi hazır olana kadar bekler.
func (p *Pool) Ready(ctx context.Context) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		p.mu.Lock()
		n := len(p.idle)
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return ErrClosed
		}
		if n > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Do, havuzdaki bir işçiye FastCGI isteği gönderir.
//
// Dönen yanıtın gövdesi akış hâlindedir ve işçi, gövde kapatılana kadar bu
// isteğe ayrılmış kalır. Çağıran Body'yi mutlaka kapatmalıdır; kapatmazsa
// işçi havuza dönmez.
func (p *Pool) Do(ctx context.Context, params map[string]string, stdin io.Reader) (*fastcgi.Response, error) {
	// En fazla iki deneme: ilk seçilen işçinin süreci tam o anda ölmüş
	// olabilir. Yalnızca hiçbir şey yazılmadan başarısız olan bağlantı
	// yeniden denenir — istek gövdesi bir kez okunduktan sonra tekrar
	// oynatılamaz.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		w, err := p.acquire(ctx)
		if err != nil {
			return nil, err
		}

		conn, err := net.DialTimeout("tcp", w.addrLocked(p), p.cfg.DialTimeout)
		if err != nil {
			p.stats.failures.Add(1)
			p.recycle(w)
			lastErr = fmt.Errorf("phppool: işçiye bağlanılamadı: %w", err)
			continue
		}

		resp, err := fastcgi.Do(ctx, conn, params, stdin)
		if err != nil {
			p.stats.failures.Add(1)
			p.recycle(w)
			return nil, err
		}

		p.stats.requests.Add(1)
		resp.Body = &pooledBody{ReadCloser: resp.Body, pool: p, worker: w}
		return resp, nil
	}
	return nil, lastErr
}

// Stats, havuzun o anki sayaçlarını döner.
type Stats struct {
	Workers  int
	Idle     int
	Requests int64
	Failures int64
	Recycled int64
	Crashed  int64
}

func (p *Pool) Stats() Stats {
	p.mu.Lock()
	idle := len(p.idle)
	workers := len(p.workers)
	p.mu.Unlock()
	return Stats{
		Workers:  workers,
		Idle:     idle,
		Requests: p.stats.requests.Load(),
		Failures: p.stats.failures.Load(),
		Recycled: p.stats.recycled.Load(),
		Crashed:  p.stats.crashed.Load(),
	}
}

// Close, tüm işçileri durdurur ve süreç grubunu kapatır. Birden çok kez
// çağrılabilir.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	waiters := p.waiters
	p.waiters = nil
	p.idle = nil
	p.mu.Unlock()

	// Bekleyenleri serbest bırak; acquire ErrClosed görecek.
	for _, ch := range waiters {
		close(ch)
	}

	p.cancel()
	p.wg.Wait()
	return p.group.Close()
}

// --- işçi tahsisi -----------------------------------------------------------
//
// Havuz, kanal yerine muteks + bekleyen kuyruğu kullanıyor. Kanalla yapılan
// basit çözümde, bir işçi kuyrukta beklerken süreci ölürse kanalda "bayat"
// bir giriş kalır ve yeniden doğan işçi ikinci kez kuyruğa girer; sonuçta tek
// bir php-cgi'ye iki istek birden düşer. Aşağıdaki yapıda işçinin hazır olup
// olmadığı ve kuyrukta bulunup bulunmadığı tek bir kilit altında tutulur.

func (p *Pool) acquire(ctx context.Context) (*worker, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		w := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return w, nil
	}
	ch := make(chan *worker, 1)
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()

	select {
	case w, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		return w, nil
	case <-ctx.Done():
		p.mu.Lock()
		for i, c := range p.waiters {
			if c == ch {
				p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
				p.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		p.mu.Unlock()
		// Kuyruktan çıkarılmışız: tam o sırada bir işçi verilmiş olabilir,
		// onu havuza geri koy ki kaybolmasın.
		if w, ok := <-ch; ok {
			p.release(w)
		}
		return nil, ctx.Err()
	}
}

// release, hazır bir işçiyi havuza geri koyar ya da bekleyen birine verir.
func (p *Pool) release(w *worker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !w.ready {
		return
	}
	for len(p.waiters) > 0 {
		ch := p.waiters[0]
		p.waiters = p.waiters[1:]
		ch <- w // tampon boyutu 1: asla bloklamaz
		return
	}
	p.idle = append(p.idle, w)
}

// markUp, süreci hazır olan işçiyi havuza sokar.
func (p *Pool) markUp(w *worker) {
	p.mu.Lock()
	w.ready = true
	w.requests = 0
	p.mu.Unlock()
	p.release(w)
}

// markDown, süreci ölen işçiyi havuzdan çeker.
func (p *Pool) markDown(w *worker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.ready = false
	for i, x := range p.idle {
		if x == w {
			p.idle = append(p.idle[:i], p.idle[i+1:]...)
			break
		}
	}
}

// recycle, işçiyi havuza döndürmeden yenilenmesini ister.
func (p *Pool) recycle(w *worker) {
	p.mu.Lock()
	w.ready = false
	p.mu.Unlock()
	w.requestRecycle()
}

// finish, istek biten işçiyi ya havuza döndürür ya da yeniler.
func (p *Pool) finish(w *worker) {
	p.mu.Lock()
	w.requests++
	spent := p.cfg.MaxRequests > 0 && w.requests >= p.cfg.MaxRequests
	p.mu.Unlock()

	if spent {
		p.recycle(w)
		return
	}
	p.release(w)
}

func (w *worker) addrLocked(p *Pool) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return w.addr
}

// pooledBody, yanıt gövdesi kapatıldığında işçiyi havuza döndürür.
type pooledBody struct {
	io.ReadCloser
	pool   *Pool
	worker *worker
	once   sync.Once
}

func (b *pooledBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.pool.finish(b.worker) })
	return err
}

// childEnv, çocuk sürecin ortamını kurar.
func (p *Pool) childEnv() []string {
	env := append([]string{}, os.Environ()...)
	// Süreç yenilemesini biz yönetiyoruz: php-cgi'nin kendi sayacı (varsayılan
	// 500) devrede kalırsa süreçler bizim haberimiz olmadan kapanır.
	env = append(env, "PHP_FCGI_MAX_REQUESTS=0")
	// php-cgi'yi tek süreç kipinde tut; kendi çocuklarını üretmesin.
	env = append(env, "PHP_FCGI_CHILDREN=0")
	env = append(env, p.cfg.Env...)
	return env
}

func (p *Pool) logf(format string, args ...any) {
	if p.cfg.Logger == nil {
		return
	}
	p.cfg.Logger.Info(fmt.Sprintf(format, args...), "havuz", p.cfg.Name)
}
