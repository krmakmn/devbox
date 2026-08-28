// Package ports, çakışmayan TCP portları tahsis eder.
//
// Windows'ta bu, göründüğünden zor. Üç ayrı sebeple bir port "boş görünüp"
// bağlanamayabiliyor:
//
//   - **Hyper-V rezervasyonları.** WSL2 ya da Docker Desktop açıkken Windows
//     geniş port aralıklarını sessizce rezerve eder. Bind çağrısı
//     "erişim engellendi" ile döner ve hiçbir süreç o portu tutuyor
//     görünmez. Kullanıcı için tamamen anlaşılmaz bir durum: netstat boş
//     gösterir ama bağlanılamaz.
//   - **Başka bir süreç dinliyordur.** IIS, ICS, kurumsal ajanlar.
//   - **Yarış.** Boş bulduğumuz port, biz kullanana kadar başkası tarafından
//     alınabilir.
//
// Üçüne karşı da tek güvenilir yöntem portu gerçekten bağlamayı denemek;
// rezervasyon listesi ise denemeden önce büyük bir aralığı elemeye yarıyor.
package ports

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Range, kapsayıcı bir port aralığı.
type Range struct {
	Start int
	End   int
}

// Contains, portun aralıkta olup olmadığını söyler.
func (r Range) Contains(port int) bool { return port >= r.Start && port <= r.End }

func (r Range) String() string { return fmt.Sprintf("%d-%d", r.Start, r.End) }

// Allocator, portları tahsis eder ve tahsis edilenleri hatırlar.
type Allocator struct {
	host string

	mu       sync.Mutex
	excluded []Range
	taken    map[int]bool
}

// New, verilen arayüz için bir tahsis edici oluşturur. Host boşsa 127.0.0.1.
func New(host string) *Allocator {
	if host == "" {
		host = "127.0.0.1"
	}
	return &Allocator{host: host, taken: make(map[int]bool)}
}

// LoadExclusions, işletim sisteminin rezerve ettiği aralıkları okur.
//
// Hata dönerse tahsis çalışmaya devam eder; yalnız rezerve aralıkları
// önceden eleyemeyiz, bağlama denemesi yine de doğruyu söyler.
func (a *Allocator) LoadExclusions(ctx context.Context) error {
	ranges, err := readExcludedRanges(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.excluded = ranges
	a.mu.Unlock()
	return nil
}

// Excluded, yüklenmiş rezervasyon aralıklarını döner.
func (a *Allocator) Excluded() []Range {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Range(nil), a.excluded...)
}

// SetExcluded, rezervasyon aralıklarını elle ayarlar (testler için).
func (a *Allocator) SetExcluded(ranges []Range) {
	a.mu.Lock()
	a.excluded = append([]Range(nil), ranges...)
	a.mu.Unlock()
}

// IsExcluded, portun işletim sistemi tarafından rezerve edilip
// edilmediğini söyler.
func (a *Allocator) IsExcluded(port int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.excluded {
		if r.Contains(port) {
			return true
		}
	}
	return false
}

// Allocate, tercih edilen porttan başlayarak kullanılabilir bir port bulur.
//
// preferred 0 ise işletim sistemine seçtirilir (geçici port). Sabit port
// istemenin sebebi, Apache ve Nginx yapılandırmasının FastCGI adreslerini
// önceden bilmek zorunda olması: her açılışta değişen bir port,
// yapılandırmayı her açılışta yeniden yazmak demek.
func (a *Allocator) Allocate(preferred int) (int, error) {
	if preferred == 0 {
		return a.allocateEphemeral()
	}

	const maxScan = 200
	var lastErr error
	for port := preferred; port < preferred+maxScan && port <= 65535; port++ {
		if a.IsExcluded(port) {
			lastErr = fmt.Errorf("port %d işletim sistemi tarafından rezerve edilmiş", port)
			continue
		}
		// Önce sahiplen, sonra dene: "boş mu?" ile "işaretle" ayrı adımlar
		// olursa iki goroutine aynı portu alır.
		if !a.claim(port) {
			continue
		}
		if err := a.tryBind(port); err != nil {
			a.Release(port)
			lastErr = err
			continue
		}
		return port, nil
	}

	return 0, fmt.Errorf("ports: %d'ten başlayarak boş port bulunamadı: %w\n%s",
		preferred, lastErr, a.diagnosis(preferred))
}

// AllocateSeries, ardışık olması gerekmeyen n adet port tahsis eder.
func (a *Allocator) AllocateSeries(preferred, n int) ([]int, error) {
	out := make([]int, 0, n)
	next := preferred
	for i := 0; i < n; i++ {
		port, err := a.Allocate(next)
		if err != nil {
			for _, p := range out {
				a.Release(p)
			}
			return nil, err
		}
		out = append(out, port)
		if next != 0 {
			next = port + 1
		}
	}
	return out, nil
}

// Release, portu tekrar kullanılabilir yapar.
func (a *Allocator) Release(port int) {
	a.mu.Lock()
	delete(a.taken, port)
	a.mu.Unlock()
}

// Taken, tahsis edilmiş portları sıralı döner.
func (a *Allocator) Taken() []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]int, 0, len(a.taken))
	for p := range a.taken {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// claim, port alınmamışsa atomik olarak işaretler.
//
// Ayrı bir "alınmış mı?" sorgusu ile "işaretle" adımı arasında başka bir
// goroutine aynı portu alabiliyor; ikisi tek kilit altında olmak zorunda.
func (a *Allocator) claim(port int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.taken[port] {
		return false
	}
	a.taken[port] = true
	return true
}

// tryBind, portu gerçekten bağlamayı dener. Rezervasyon listesi ve netstat
// yanıltabilir; bağlama denemesi yanıltmaz.
func (a *Allocator) tryBind(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(a.host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

// allocateEphemeral, işletim sisteminden boş bir port ister.
//
// Naif hâli yarışlı: dinleyiciyi kapatıp portu döndürünce, aynı anda çalışan
// başka bir çağrı işletim sisteminden aynı portu alabiliyor. Çakışma
// görürsek dinleyiciyi açık tutup yeniden deniyoruz — açık kaldığı sürece
// işletim sistemi o portu bir daha vermez. Tutulan dinleyiciler dönüşte
// kapanıyor.
func (a *Allocator) allocateEphemeral() (int, error) {
	var held []net.Listener
	defer func() {
		for _, ln := range held {
			ln.Close()
		}
	}()

	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(a.host, "0"))
		if err != nil {
			return 0, fmt.Errorf("ports: geçici port alınamadı: %w", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port

		if a.claim(port) {
			// İşaretleme kapatmadan önce yapılıyor: kapattıktan sonra
			// aynı portu alan bir çağrı, işaretimizi görüp yeniden
			// deneyecek.
			ln.Close()
			return port, nil
		}
		held = append(held, ln)
	}
	return 0, fmt.Errorf("ports: %d denemede çakışmayan geçici port bulunamadı", maxAttempts)
}

// diagnosis, port bulunamadığında kullanıcıya ne olduğunu anlatır.
//
// "port bulunamadı" tek başına hiçbir şey söylemiyor; asıl mesele hangi
// sebeple bulunamadığı ve ne yapılacağı.
func (a *Allocator) diagnosis(port int) string {
	var sb strings.Builder
	if a.IsExcluded(port) {
		sb.WriteString("Bu aralık işletim sistemi tarafından rezerve edilmiş.\n")
		sb.WriteString("Genellikle Hyper-V, WSL2 ya da Docker Desktop açıkken olur.\n")
		sb.WriteString("Görmek için: netsh int ipv4 show excludedportrange protocol=tcp\n")
		if ranges := a.Excluded(); len(ranges) > 0 {
			names := make([]string, 0, len(ranges))
			for _, r := range ranges {
				names = append(names, r.String())
			}
			sb.WriteString("Rezerve aralıklar: " + strings.Join(names, ", ") + "\n")
		}
		return sb.String()
	}
	sb.WriteString("Portu başka bir süreç tutuyor olabilir.\n")
	if port == 80 || port == 443 {
		sb.WriteString("80 ve 443 için en sık sebep IIS (W3SVC) ya da\n")
		sb.WriteString("\"World Wide Web Publishing Service\". Durdurmayı deneyin.\n")
	}
	sb.WriteString("Görmek için: netstat -ano | findstr :" + strconv.Itoa(port) + "\n")
	return sb.String()
}

// ParseExcludedRanges, netsh çıktısını çözer.
//
//	Protocol tcp Port Exclusion Ranges
//
//	Start Port    End Port
//	----------    --------
//	      1024        1123
//	      1124        1223
//
//	* - Administered port exclusions.
//
// Ayrıştırma kasten hoşgörülü: iki sayı içeren her satır bir aralık sayılır.
// Windows sürümleri arasında başlık metni ve boşluklar değişiyor, sayı
// çiftleri değişmiyor.
func ParseExcludedRanges(output string) []Range {
	var out []Range
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		start, err1 := strconv.Atoi(fields[0])
		end, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if start < 1 || end > 65535 || start > end {
			continue
		}
		out = append(out, Range{Start: start, End: end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}
