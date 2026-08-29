package edge

import (
	"net"
	"net/http"
	"strings"
)

// LoopbackOnly, yalnız aynı makineden gelen isteklere izin verir.
//
// # Neden gerekiyor
//
// Kenar vekili 80 ve 443'ü tüm arayüzlerde dinliyor; bu kasıtlı, çünkü
// siteyi aynı ağdaki bir telefondan denemek geliştirmenin olağan bir
// parçası. Ama kenarın arkasında yalnız site yok: posta kutusu
// yakalanmış postaları, HTTP denetleyicisi ise kaydedilmiş istek
// gövdelerini ve Authorization başlıklarını gösteriyor.
//
// Yani "siteyi ağa aç" kararı, hata ayıklama yüzeylerini de açıyordu.
// Bu sarmalayıcı ikisini ayırıyor: site herkese açık kalıyor, hata
// ayıklama yüzeyleri yalnız makinenin kendisinden erişiliyor.
//
// # Neden RemoteAddr, Host değil
//
// Host başlığı istemcinin yazdığı bir metin; onunla karar vermek,
// saldırganın kendi başlığını yazmasıyla atlatılır. RemoteAddr soketin
// karşı ucu — istemci onu değiştiremez.
func LoopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackAddr(r.RemoteAddr) {
			http.Error(w,
				"Bu sayfa yalnız DevBox'ın çalıştığı makineden açılabilir.\n"+
					"Yakalanan postalar ve kaydedilen istekler ağa açılmıyor.",
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackAddr, "ip:port" biçimindeki adresin geri döngü olup
// olmadığını söyler.
func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		// Adres çözülemiyorsa güvenli tarafta kal.
		return false
	}
	return ip.IsLoopback()
}
