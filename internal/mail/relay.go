package mail

import (
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Relayer, izin verilen alıcılara postayı gerçekten gönderir.
//
// # Neden varsayılan olarak kapalı ve neden beyaz liste
//
// Yakalayıcının varlık sebebi, geliştirme ortamındaki postanın dışarı
// çıkmamasıdır: test verisinde gerçek bir adres bulunur ve o adrese
// gerçekten posta gitmesi geri alınamaz. Röle bu güvenceyi deldiği için
// açıkça yazılmadıkça çalışmıyor ve yazıldığında da yalnız listelenen
// alıcılara gidiyor. Liste boşsa yapılandırma reddediliyor — "hepsine
// gönder" diye bir kısayol yok.
//
// Röle edilen posta da yakalanıyor. Kullanıcı arayüzde hem gövdeyi hem
// rölenin sonucunu görüyor; "gitti mi gitmedi mi" sorusu cevapsız
// kalmıyor.
type Relayer struct {
	// Host, gerçek SMTP sunucusu ("smtp.example.com:587").
	Host string

	// Username ve Password, sunucu kimlik doğrulaması istiyorsa.
	//
	// Parola devbox.yaml'dan değil ortam değişkeninden geliyor:
	// devbox.yaml depoya giriyor ve parolanın depoda işi yok.
	Username string
	Password string

	// Allow, röle edilecek alıcılar. Tam adres ("kerim@sirket.com") ya da
	// alan adı ("sirket.com") yazılabilir.
	Allow []string

	Logger *slog.Logger

	// send, testte değiştirilebilsin diye.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

	mu sync.Mutex
}

// RelayResult, bir postanın röle sonucunu anlatır.
type RelayResult struct {
	// Recipients, röle edilen alıcılar.
	Recipients []string `json:"recipients"`

	// Skipped, listede olmadığı için röle edilmeyen alıcılar.
	Skipped []string `json:"skipped"`

	// Error, gönderim başarısızsa hata metni.
	Error string `json:"error,omitempty"`

	At time.Time `json:"at"`
}

// Validate, yapılandırmanın kullanılabilir olduğunu denetler.
func (r *Relayer) Validate() error {
	if r.Host == "" {
		return fmt.Errorf("röle sunucusu adresi yok")
	}
	if !strings.Contains(r.Host, ":") {
		return fmt.Errorf("röle sunucusu host:port biçiminde olmalı: %q", r.Host)
	}
	if len(r.Allow) == 0 {
		return fmt.Errorf("röle için izin listesi zorunlu: hangi alıcılara gerçekten posta gideceği açıkça yazılmalı")
	}
	for _, a := range r.Allow {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("röle izin listesinde boş girdi var")
		}
	}
	if r.Username != "" && r.Password == "" {
		return fmt.Errorf("röle kullanıcı adı verilmiş ama parola yok")
	}
	return nil
}

// allowed, alıcının listede olup olmadığını söyler.
//
// Karşılaştırma tam adres ya da alan adı üzerinden; alt alan adları
// kapsanmıyor. "sirket.com" yazan biri "test.sirket.com"a posta gitmesini
// istemiş sayılmaz — burada geniş yorum, geri alınamayan bir postaya
// dönüşür.
func (r *Relayer) allowed(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	domain := addr
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		domain = addr[i+1:]
	}
	for _, a := range r.Allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == addr || a == domain {
			return true
		}
	}
	return false
}

// Relay, postayı izin verilen alıcılara gönderir.
//
// Hiçbir alıcı listede değilse nil döner: yapılacak bir iş yok.
func (r *Relayer) Relay(msg *Message) *RelayResult {
	result := &RelayResult{At: time.Now()}
	for _, to := range msg.To {
		if r.allowed(to) {
			result.Recipients = append(result.Recipients, to)
		} else {
			result.Skipped = append(result.Skipped, to)
		}
	}
	if len(result.Recipients) == 0 {
		return nil
	}

	sendFn := r.send
	if sendFn == nil {
		sendFn = smtp.SendMail
	}

	var auth smtp.Auth
	if r.Username != "" {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		// PlainAuth, sunucu TLS'e geçmediyse kimlik bilgisini
		// göndermeyi reddediyor; bu bizim istediğimiz davranış.
		auth = smtp.PlainAuth("", r.Username, r.Password, host)
	}

	// Aynı anda tek gönderim: bir geliştirme ortamından çıkan posta
	// hacmi düşük, buna karşılık çoğu sağlayıcı eşzamanlı oturumu
	// sınırlıyor.
	r.mu.Lock()
	err := sendFn(r.Host, auth, msg.From, result.Recipients, msg.Raw)
	r.mu.Unlock()

	if err != nil {
		result.Error = err.Error()
		r.logger().Error("posta röle edilemedi",
			"alıcılar", result.Recipients, "hata", err)
		return result
	}
	r.logger().Info("posta röle edildi", "alıcılar", result.Recipients)
	return result
}

func (r *Relayer) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
