// Package procstat, çalışan bir sürecin bellek ve işlemci kullanımını
// okur.
//
// # Neden yalnız sürecin kendisi
//
// Bir projenin toplam kaynak kullanımı, "devbox up" sürecinin altındaki
// php-cgi işçilerini, veritabanını ve web sunucusunu da kapsardı. Süreç
// ağacını toplamak Linux'ta /proc'u taramak, Windows'ta anlık görüntü
// API'siyle bütün süreç listesini gezmek demek — Windows tarafı bu
// ortamda çalıştırılıp doğrulanamayacak, hatırı sayılır miktarda
// syscall kodu. Bu yüzden ölçüm sürecin kendisiyle sınırlı ve arayüzde
// böyle etiketleniyor. Ağaç toplamı, gerçekten gerektiğinde ve gerçek
// Windows'ta denenebildiğinde eklenir.
//
// # İşlemci neden yüzde değil, birikmiş süre
//
// Yüzde iki ölçüm arasındaki farktan çıkar; hangi aralığın kullanılacağı
// ölçen tarafın kararı. Burada birikmiş işlemci süresi dönüyor, oranı
// isteyen (denetim paneli iki yoklama arasında) kendi hesaplıyor. Böylece
// paket durum tutmuyor.
package procstat

import "errors"

// ErrUnsupported, bu işletim sisteminde ölçüm yapılamıyor.
var ErrUnsupported = errors.New("procstat: bu işletim sisteminde desteklenmiyor")

// Stat, bir sürecin anlık kaynak kullanımı.
type Stat struct {
	// RSS, sürecin fiziksel bellekte tuttuğu bayt.
	RSS uint64 `json:"rss"`

	// CPUSeconds, sürecin doğduğundan beri harcadığı işlemci süresi
	// (kullanıcı + çekirdek), saniye.
	CPUSeconds float64 `json:"cpuSeconds"`
}

// Read, verilen süreç kimliğinin kullanımını okur.
func Read(pid int) (Stat, error) {
	if pid <= 0 {
		return Stat{}, errors.New("procstat: geçersiz süreç kimliği")
	}
	return read(pid)
}
