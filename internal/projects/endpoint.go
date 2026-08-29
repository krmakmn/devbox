package projects

import (
	"encoding/json"
	"strings"
)

// EndpointPrefix, "devbox up"ın paylaşılan kenara kendini tanıttığı
// satırın önekidir.
//
// Neden günlük satırı: proje ayrı bir süreç olarak çalışıyor ve
// aralarındaki tek kanal zaten denetçinin okuduğu çıktı. Hazır olma
// ölçütü de aynı kanaldan geliyor (ReadyLine). Ayrı bir soket ya da
// dosya açmak, beraberinde eşzamanlama ve temizlik sorunları getirirdi.
const EndpointPrefix = "devbox-uc: "

// Endpoint, bir projenin paylaşılan kenara bildirdiği bilgilerdir.
type Endpoint struct {
	// Addr, projenin kendi işleyicisini düz HTTP ile sunduğu loopback
	// adresi. TLS'i paylaşılan kenar sonlandırıyor.
	Addr string `json:"addr"`

	// Hosts, bu projeye yönlendirilecek alan adları.
	Hosts []string `json:"hosts"`

	// LocalOnly, yalnız makinenin kendisinden açılabilecek alan adları:
	// posta kutusu ve denetleyici.
	//
	// Ayrı tutulmaları bir güvenlik gereği. Paylaşılan kenar araya
	// girince proje sürecinin gördüğü uzak adres her zaman 127.0.0.1
	// oluyor; yani içerideki loopback denetimi her isteği geçirir.
	// Kısıtı, gerçek istemciyi gören taraf — kenar — uygulamak zorunda.
	LocalOnly []string `json:"localOnly"`
}

// FormatEndpoint, bildirim satırını üretir.
func FormatEndpoint(e Endpoint) (string, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return EndpointPrefix + string(b), nil
}

// ParseEndpoint, verilen çıktıdaki SON bildirim satırını çözer.
//
// Sonuncusu alınıyor çünkü denetçi süreci yeniden başlatabiliyor ve
// günlük tamponu önceki koşuyu da tutuyor. Eski adrese yönlendirmek,
// kullanıcının göreceği en kafa karıştırıcı hata olurdu: site açılıyor
// görünür ama bağlantı reddedilir.
func ParseEndpoint(output string) (Endpoint, bool) {
	var last string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, EndpointPrefix); ok {
			last = rest
		}
	}
	if last == "" {
		return Endpoint{}, false
	}
	var e Endpoint
	if err := json.Unmarshal([]byte(last), &e); err != nil {
		return Endpoint{}, false
	}
	if e.Addr == "" || len(e.Hosts)+len(e.LocalOnly) == 0 {
		return Endpoint{}, false
	}
	return e, true
}

// AllHosts, projenin sahiplendiği bütün alan adlarını döner.
func (e Endpoint) AllHosts() []string {
	all := make([]string, 0, len(e.Hosts)+len(e.LocalOnly))
	all = append(all, e.Hosts...)
	all = append(all, e.LocalOnly...)
	return all
}
