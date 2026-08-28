// Package supervisor, DevBox'ın yönettiği yardımcı süreçleri (mysqld, httpd,
// nginx, mailpit...) ayakta tutar.
//
// phppool'dan farkı: orada aynı işi yapan bir işçi havuzu var ve istek
// dağıtımı da onun sorumluluğunda. Burada tek tek servisler var; her birinin
// kendi hazır olma ölçütü, kendi yeniden başlatma ilkesi ve kendi günlüğü.
//
// Verilen garantiler:
//
//   - Süreç arkada kalmaz: hepsi ortak bir iş nesnesinde doğar (bkz. proc).
//   - "Çalışıyor" demeden önce gerçekten hazır olması beklenir; port
//     dinlemeye başlamamış bir MySQL'e bağlanmaya çalışmak, kullanıcının
//     göreceği ilk hatanın anlamsız olması demek.
//   - Çöken süreç üstel geri çekilmeyle yeniden başlatılır; sürekli çöken
//     bir süreç makineyi yakmaz.
//   - Son çıktısı halka tamponunda tutulur; çökme sebebini görmek için
//     günlük dosyası aramak gerekmez.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// State, bir servisin durumu.
type State int

const (
	// StateStopped, servis durdurulmuş ya da hiç başlatılmamış.
	StateStopped State = iota
	// StateStarting, süreç başlatıldı ama henüz hazır değil.
	StateStarting
	// StateRunning, süreç çalışıyor ve hazır olma ölçütünü geçti.
	StateRunning
	// StateBackoff, süreç çöktü, yeniden başlatma bekleniyor.
	StateBackoff
	// StateFailed, kalıcı olarak başarısız (yeniden başlatma sınırı doldu).
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "durdu"
	case StateStarting:
		return "başlıyor"
	case StateRunning:
		return "çalışıyor"
	case StateBackoff:
		return "yeniden başlatılacak"
	case StateFailed:
		return "başarısız"
	default:
		return "bilinmiyor"
	}
}

// RestartPolicy, çöken sürecin ne olacağını belirler.
type RestartPolicy int

const (
	// RestartAlways, süreç her çöktüğünde yeniden başlatılır. Uzun ömürlü
	// servisler (mysqld, httpd) için.
	RestartAlways RestartPolicy = iota
	// RestartNever, süreç çökerse öyle kalır. Tek seferlik işler için
	// (initdb, migration).
	RestartNever
)

// ReadyCheck, sürecin hazır olup olmadığını sınar.
type ReadyCheck interface {
	// Ready, hazırsa nil döner. Hazır değilse hata döner ve tekrar denenir.
	Ready(ctx context.Context) error
	// Describe, kullanıcıya gösterilecek açıklama.
	Describe() string
}

// TCPReady, bir adres bağlantı kabul edene kadar bekler.
//
// Çoğu servis için yeterli ve en ucuz ölçüt. Yetmediği yer: MySQL portu
// açtıktan sonra bir süre daha bağlantıyı reddeder; orada protokol düzeyi
// bir sınama gerekir.
type TCPReady struct {
	Addr string
}

func (t TCPReady) Ready(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (t TCPReady) Describe() string { return t.Addr + " dinleniyor" }

// LogReady, sürecin çıktısında bir metin görene kadar bekler.
//
// Port dinlemeyen ya da hazır olduğunu yalnız günlüğe yazan servisler için
// (ör. PostgreSQL'in "database system is ready to accept connections").
type LogReady struct {
	Substring string
	logs      *LogBuffer
}

func (l LogReady) Ready(context.Context) error {
	if l.logs == nil {
		return errors.New("supervisor: günlük tamponu yok")
	}
	if strings.Contains(l.logs.String(), l.Substring) {
		return nil
	}
	return fmt.Errorf("günlükte %q henüz yok", l.Substring)
}

func (l LogReady) Describe() string { return fmt.Sprintf("günlükte %q", l.Substring) }

// ImmediateReady, süreç başlar başlamaz hazır sayar.
//
// systemd'nin Type=simple davranışının karşılığı: "çatallandıysa
// başlamıştır". Bunun anlamı, hemen ardından çöken bir süreç için Start'ın
// başarı dönmesidir — çöküş yeniden başlatma döngüsünde görülür, Start'ta
// değil. Başlatmanın gerçekten doğrulanması gerekiyorsa TCPReady ya da
// LogReady kullanın.
type ImmediateReady struct{}

func (ImmediateReady) Ready(context.Context) error { return nil }
func (ImmediateReady) Describe() string            { return "hemen" }

// Config, tek bir servisin tanımı.
type Config struct {
	// Name, günlüklerde ve API'de görünen ad.
	Name string

	// Exec ve Args, çalıştırılacak komut.
	Exec string
	Args []string

	// Env, ek ortam değişkenleri ("K=V").
	Env []string

	// WorkDir, çalışma dizini.
	WorkDir string

	// Ready, hazır olma ölçütü. Boşsa ImmediateReady.
	Ready ReadyCheck

	// StartTimeout, hazır olma için tanınan süre.
	StartTimeout time.Duration

	// StopTimeout, nazikçe durması için tanınan süre. Sonunda öldürülür.
	StopTimeout time.Duration

	// Restart, çöken sürecin ilkesi.
	Restart RestartPolicy

	// RestartBackoff / MaxRestartBackoff, yeniden başlatma aralığı.
	RestartBackoff    time.Duration
	MaxRestartBackoff time.Duration

	// MaxRestarts, üst üste kaç başarısız denemeden sonra vazgeçilir.
	// 0 ise sınırsız.
	MaxRestarts int

	// HealthyUptime, sürecin "sağlıklı çalıştı" sayılacağı süre. Bunu aşan
	// bir süreç öldüğünde geri çekilme ve deneme sayacı sıfırlanır.
	HealthyUptime time.Duration

	// LogLimit, halka tamponunda tutulacak bayt sayısı.
	LogLimit int
}

func (c *Config) applyDefaults() {
	if c.Ready == nil {
		c.Ready = ImmediateReady{}
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = 30 * time.Second
	}
	if c.StopTimeout <= 0 {
		c.StopTimeout = 10 * time.Second
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
	if c.LogLimit <= 0 {
		c.LogLimit = 64 * 1024
	}
}

// Status, bir servisin o anki durumu.
type Status struct {
	Name     string    `json:"name"`
	State    string    `json:"state"`
	PID      int       `json:"pid,omitempty"`
	Since    time.Time `json:"since"`
	Restarts int       `json:"restarts"`
	LastErr  string    `json:"lastError,omitempty"`
	Ready    string    `json:"readyCheck"`
}

// LogBuffer, son N baytı tutan, eşzamanlı yazıma açık bir tampondur.
//
// Abonelere de dağıtır: API üzerinden canlı günlük akışı bunun üstünde
// duracak.
type LogBuffer struct {
	mu     sync.Mutex
	buf    []byte
	limit  int
	subs   map[int]chan []byte
	nextID int
}

// NewLogBuffer, verilen bayt sınırıyla bir tampon oluşturur.
func NewLogBuffer(limit int) *LogBuffer {
	if limit <= 0 {
		limit = 64 * 1024
	}
	return &LogBuffer{limit: limit, subs: make(map[int]chan []byte)}
}

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	line := append([]byte(nil), p...)
	subs := make([]chan []byte, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		// Bloklamıyoruz: yavaş bir abone yüzünden süreç çıktısı
		// tıkanmamalı. Yetişemeyen abone satır kaybeder.
		select {
		case ch <- line:
		default:
		}
	}
	return len(p), nil
}

// Bytes, tamponun kopyasını döner.
func (b *LogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *LogBuffer) String() string { return string(b.Bytes()) }

// Subscribe, yeni satırları alacak bir kanal döner. Dönen fonksiyon
// aboneliği sonlandırır.
func (b *LogBuffer) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
		b.mu.Unlock()
	}
}
