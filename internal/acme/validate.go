package acme

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// validationTimeout, tek bir doğrulama isteğine tanınan süre.
//
// Yerel ağda milisaniyeler sürüyor; uzun bir zaman aşımı, yanlış
// yapılandırılmış bir istemcide kullanıcıyı bekletmekten başka işe
// yaramaz.
const validationTimeout = 5 * time.Second

// validateHTTP01, meydan okumayı alan adının 80. portundan doğrular.
//
// # Neden adres yönlendirmesi var
//
// Standart akışta doğrulayıcı, alan adını çözüp 80. porta bağlanır.
// Bizde alan adı zaten 127.0.0.1'e çözülüyor ve orada DevBox'ın kenar
// vekili duruyor — yani konteynerdeki istemcinin sunduğu meydan okumaya
// değil, kenarın verdiği 404'e bakardık. Config.Resolve bu yüzden var:
// bir alan adı için "doğrulamayı şu adrese yap" demeyi sağlıyor.
func (s *Server) validateHTTP01(domain, token, expected string) error {
	target := net.JoinHostPort(domain, "80")
	if s.cfg.Resolve != nil {
		if addr, ok := s.cfg.Resolve(domain); ok {
			target = addr
		}
	}

	url := fmt.Sprintf("http://%s/.well-known/acme-challenge/%s", domain, token)

	// Bağlantı hedefi ile Host başlığı ayrı: istek alan adına gidiyormuş
	// gibi görünüyor (sanal konak eşleşsin diye) ama soket yönlendirilen
	// adrese açılıyor.
	dialer := &net.Dialer{Timeout: validationTimeout}
	client := &http.Client{
		Timeout: validationTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, target)
			},
			DisableKeepAlives: true,
		},
		// Yönlendirme izlenmiyor: meydan okuma tam o adreste sunulmalı.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s adresine ulaşılamadı (%s): %w", url, target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s adresi %d döndü", url, resp.StatusCode)
	}

	// Gövde sınırlı: doğrulama yanıtı birkaç yüz bayt; sınırsız okumak
	// kötü niyetli bir istemcinin belleği doldurmasına yol açardı.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("yanıt okunamadı: %w", err)
	}
	got := strings.TrimSpace(string(body))
	if got != expected {
		return fmt.Errorf("beklenen anahtar yetkilendirmesi gelmedi (%d bayt okundu)", len(got))
	}
	return nil
}
