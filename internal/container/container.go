// Package container, servisleri konteynerde çalıştırır.
//
// # Neden konteyner sürücüsü
//
// Bir geliştirme ortamının her bileşeni yerel ikili olarak kurulamıyor.
// Windows'ta resmî derlemesi olmayan (RabbitMQ, Elasticsearch,
// ClickHouse) ya da kurulumu makineyi kirleten şeyler var. Ekibin geri
// kalanı zaten bir imaj kullanıyorsa, aynı imajı çalıştırmak "bende
// çalışıyor" farklarını da kapatıyor.
//
// # Neden ön planda "docker run", arka planda değil
//
// Konteyneri "docker run -d" ile başlatıp ayrıca günlük ve durum sormak
// yerine, "docker run" istemcisini denetçinin altında ön planda
// çalıştırıyoruz. Böylece konteynerin çıktısı doğrudan servisin halka
// tamponuna düşüyor, süreç ömrü konteyner ömrüyle eşleşiyor ve yeniden
// başlatma ilkesi, hazır olma ölçütü, durum bildirimi gibi her şey
// denetçiden geliyor — ikinci bir yaşam döngüsü yönetimi yazmıyoruz.
//
// Bedeli: docker istemcisi öldürülürse konteyner arkada kalabiliyor. Bu
// yüzden --rm veriliyor ve kapanışta ad üzerinden zorla siliniyor.
package container

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// runtimes, aranacak konteyner çalıştırıcıları.
//
// Podman docker ile büyük ölçüde komut uyumlu; Docker Desktop dışında
// bir seçenek isteyenler için ikisini de arıyoruz.
var runtimes = []string{"docker", "podman"}

// ErrNoRuntime, ne docker ne podman bulunabildi.
type ErrNoRuntime struct{}

func (ErrNoRuntime) Error() string {
	return "container: docker ya da podman bulunamadı.\n" +
		"  Windows'ta kurulum: https://docs.docker.com/desktop/install/windows-install/"
}

// FindRuntime, kullanılabilir çalıştırıcıyı bulur.
func FindRuntime() (string, error) {
	for _, name := range runtimes {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", ErrNoRuntime{}
}

// Spec, çalıştırılacak konteyner.
type Spec struct {
	Project string
	Name    string

	// Image, çalıştırılacak imaj.
	Image string

	// ContainerPort, konteynerin içinde dinlenen port.
	ContainerPort int

	// HostPort, geri döngüde yayınlanacak port.
	HostPort int

	Env     map[string]string
	Volumes []string
	Command []string

	// WorkDir, göreli bağlamaların çözüleceği dizin.
	WorkDir string
}

// ContainerName, konteynerin adı.
//
// Ad öngörülebilir olmalı: kapanışta ya da bir çökme sonrasında arkada
// kalmış konteyneri bulup silebilmek buna dayanıyor.
func (s Spec) ContainerName() string {
	return "devbox-" + sanitize(s.Project) + "-" + sanitize(s.Name)
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func sanitize(s string) string {
	return strings.Trim(unsafeName.ReplaceAllString(s, "-"), "-")
}

// Args, "docker run" argümanlarını üretir.
//
// Port yalnız 127.0.0.1'e yayınlanıyor. "-p 8080:80" yazmak konteyneri
// makinenin tüm arayüzlerinde açar ve aynı ağdaki herkes geliştirme
// veritabanınıza bağlanabilir; Docker bu öntanımlıyla güvenlik duvarı
// kurallarını da atladığı için sık yaşanan bir kazadır.
func (s Spec) Args() []string {
	args := []string{
		"run", "--rm",
		"--name", s.ContainerName(),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", s.HostPort, s.ContainerPort),
	}
	// Sıralı: aynı yapılandırma her çalıştırmada aynı komutu üretsin.
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "-e", key+"="+s.Env[key])
	}
	for _, vol := range s.Volumes {
		host, target, ok := splitVolume(vol)
		if !ok {
			continue
		}
		if !filepath.IsAbs(host) {
			host = filepath.Join(s.WorkDir, filepath.FromSlash(host))
		}
		args = append(args, "-v", host+":"+target)
	}
	args = append(args, s.Image)
	args = append(args, s.Command...)
	return args
}

// splitVolume, "kaynak:hedef[:seçenek]" dizisini ana makine yolu ve
// gerisi olarak ayırır.
//
// İlk iki nokta üst üsteden bölmek Windows'ta yanlış sonuç veriyordu:
// "C:\\kod\\veri:/data" için ana makine yolu "C" oluyor, gerisi
// "\\kod\\veri:/data". Sonra filepath.IsAbs("C") false döndüğü için yol
// çalışma dizinine ekleniyor ve bağlama sessizce bambaşka bir yeri
// gösteriyordu. Windows birincil hedefimiz olduğu için bu gerçek bir
// kusurdu — üstelik sessiz olanından.
//
// filepath.VolumeName tam da bu işi platforma göre yapıyor: Windows'ta
// "C:" ya da "\\\\sunucu\\pay" döner, Unix'te boş. Böylece GOOS'a elle
// bakmaya gerek kalmıyor ve Unix'te "c:/data" (adı "c" olan göreli
// dizin) hâlâ doğru okunuyor.
//
// Hedef tarafı olduğu gibi bırakılıyor: "/data:ro" gibi seçenekler
// zincirin sonuna eklendiği için kendiliğinden korunuyor.
func splitVolume(vol string) (host, rest string, ok bool) {
	start := len(filepath.VolumeName(vol))
	i := strings.Index(vol[start:], ":")
	if i < 0 {
		return "", "", false
	}
	i += start
	return vol[:i], vol[i+1:], true
}

// ServiceConfig, denetçi yapılandırmasını üretir.
func ServiceConfig(runtime string, spec Spec) supervisor.Config {
	return supervisor.Config{
		Name: "servis-" + spec.Name,
		Exec: runtime,
		Args: spec.Args(),
		// Konteynerin içindeki uygulama portu açtığında hazır sayıyoruz.
		// Günlük satırına bakamayız: imajın ne yazacağını bilmiyoruz.
		Ready:         supervisor.TCPReady{Addr: fmt.Sprintf("127.0.0.1:%d", spec.HostPort)},
		StartTimeout:  90 * time.Second,
		StopTimeout:   20 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 20 * time.Second,
	}
}

// Remove, arkada kalmış bir konteyneri siler.
func Remove(ctx context.Context, runtime, name string) error {
	out, err := exec.CommandContext(ctx, runtime, "rm", "-f", name).CombinedOutput()
	if err != nil {
		// Konteyner yoksa bu bir hata değil.
		if strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil
		}
		return fmt.Errorf("container: %s silinemedi: %w (%s)",
			name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ImageExists, imajın yerelde olup olmadığını söyler.
func ImageExists(ctx context.Context, runtime, image string) bool {
	return exec.CommandContext(ctx, runtime, "image", "inspect", image).Run() == nil
}

// Pull, imajı indirir.
func Pull(ctx context.Context, runtime, image string) error {
	out, err := exec.CommandContext(ctx, runtime, "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("container: %s indirilemedi: %w\n%s",
			image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Endpoint, kenar vekilinin yönlendireceği adres.
func (s Spec) Endpoint() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.HostPort)
}
