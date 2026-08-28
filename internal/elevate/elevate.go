// Package elevate, yönetici hakkı gerektiren işlemleri yürütür.
//
// # Neden ayrıcalıklı yardımcı servis yok
//
// Yol haritası, LocalSystem olarak koşan kalıcı bir yardımcı servis ve
// adlandırılmış boru üzerinden IPC öngörüyordu. Faz 1'de ayrıcalıklı işlem
// listesi tek tek gözden geçirilince o tasarımın gerekçesi kalmadı:
//
//	80/443 bağlama    → Windows'ta 1024 altı portlar ayrıcalıklı değil.
//	                    Ayrıcalıklı işlem değilmiş.
//	kök sertifika     → Windows onay penceresi gösteriyor; bir servis
//	                    masaüstü oturumuna pencere gösteremez. Servisten
//	                    yapılamaz (Faz 0 bulgusu).
//	Hyper-V port      → Sorgu ayrıcalık istemiyor.
//	  aralığı sorgusu
//	NRPT / hosts      → Yönetici gerekiyor ama yılda birkaç kez.
//	güvenlik duvarı   → Yönetici gerekiyor ama kurulumda bir kez.
//	servis kaydı      → Yönetici gerekiyor ama kurulumda bir kez.
//
// Geriye kalan üç işlem de seyrek ve tek seferlik. Onlar için kalıcı bir
// ayrıcalıklı dinleyici çalıştırmak, projenin en büyük güvenlik yüzeyini
// (yerel ayrıcalık yükseltme) sürekli açık tutmak demek — üstelik yılda
// birkaç dakika kullanılacak bir yetenek için.
//
// Onun yerine talep üzerine yükseltme: DevBox kendini yalnız o işlem için,
// tipi ve içeriği doğrulanmış argümanlarla yeniden başlatıyor. Sürekli
// dinleyen bir ayrıcalıklı süreç yok, dolayısıyla saldırılacak IPC yüzeyi de
// yok. Bedeli işlem başına bir UAC penceresi; seyrek işlemler için makul.
package elevate

import (
	"errors"
	"strings"
)

// ErrDeclined, kullanıcı yükseltme istemini reddettiğinde döner.
var ErrDeclined = errors.New("elevate: yükseltme isteği reddedildi")

// ErrUnsupported, platform yükseltmeyi desteklemediğinde döner.
var ErrUnsupported = errors.New("elevate: bu platformda desteklenmiyor")

// buildCommandLine, argümanları Windows'un komut satırı kurallarına göre
// tek bir dizeye çevirir.
//
// Windows'ta süreçlere argüman dizisi değil tek bir dize geçilir; ayrıştırma
// alıcı tarafta yapılır. Kuralları yanlış uygulamak, boşluk içeren bir yolun
// iki argümana bölünmesi ya da daha kötüsü, tırnak kaçıran bir değerin
// komuta argüman eklemesi demek.
//
// Kurallar (CommandLineToArgvW): tırnaktan hemen önceki ters bölüler
// ikilenir; tırnak kaçırılır; boşluk ya da tırnak içeren argüman tırnak
// içine alınır.
func buildCommandLine(args []string) string {
	var sb strings.Builder
	for i, arg := range args {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(quoteArg(arg))
	}
	return sb.String()
}

func quoteArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, ` "	`) {
		return arg
	}

	var sb strings.Builder
	sb.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			// Tırnaktan önceki ters bölüler ikilenir, sonra tırnak kaçırılır.
			sb.WriteString(strings.Repeat(`\`, backslashes*2+1))
			sb.WriteByte('"')
			backslashes = 0
		default:
			sb.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			sb.WriteRune(r)
		}
	}
	// Kapanış tırnağından önceki ters bölüler de ikilenir.
	sb.WriteString(strings.Repeat(`\`, backslashes*2))
	sb.WriteByte('"')
	return sb.String()
}
