// Package api, devboxd'nin yerel HTTP arayüzüdür.
//
// Tasarım ilkesi: GUI'nin yapabildiği her şey CLI'dan da yapılabilsin. Bunun
// yolu, ikisinin de aynı API'yi kullanması. İş mantığı burada değil, altta
// duran paketlerde; API yalnız onları dışarı açıyor.
//
// # Güvenlik
//
// Sunucu yalnız 127.0.0.1'i dinliyor ama bu tek başına yeterli değil:
// makinede çalışan herhangi bir tarayıcı sayfası da localhost'a istek
// atabilir. Bu yüzden üç katman var:
//
//   - Jeton: her istek Authorization başlığıyla geliyor. Jeton dosyası
//     yalnız kullanıcının okuyabileceği izinlerle diskte duruyor.
//   - Host denetimi: DNS yeniden bağlama (rebinding) saldırısında saldırgan
//     kendi alan adını 127.0.0.1'e çözdürüp tarayıcıdan bize istek attırır.
//     Host başlığını doğrulamak bunu keser.
//   - Tarayıcıdan gelen istekler için CORS başlığı yok; ön kontrol (preflight)
//     gerektiren istekler tarayıcı tarafından engellenir.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tokenBytes, jetonun ham uzunluğu. 32 bayt, kaba kuvvete karşı fazlasıyla
// yeterli.
const tokenBytes = 32

// GenerateToken, yeni bir rastgele jeton üretir.
func GenerateToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("api: jeton üretilemedi: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// LoadOrCreateToken, jeton dosyasını okur; yoksa üretip yazar.
//
// Dosya izinleri 0600: aynı makinedeki başka bir kullanıcı jetonu okuyup
// servisleri yönetebilmemeli. (Windows'ta izin modeli farklı çalışır;
// oradaki asıl koruma dosyanın kullanıcı profilinde durması.)
func LoadOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if token != "" {
			return token, nil
		}
	}

	token, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("api: jeton yazılamadı: %w", err)
	}
	return token, nil
}

// ReadToken, var olan jetonu okur.
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("api: jeton okunamadı (devboxd çalışıyor mu?): %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("api: jeton dosyası boş")
	}
	return token, nil
}

// tokenMatches, jetonları sabit sürede karşılaştırır.
//
// Basit bir eşitlik karşılaştırması, jetonun kaç karakterinin doğru olduğunu
// zamanlamayla sızdırır. Yerel bir arayüz için abartı gibi görünse de doğru
// olanı yapmanın maliyeti sıfır.
func tokenMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
