// Package services, projenin yanında çalışan yardımcı servisleri
// (Redis, Meilisearch, MinIO) ayağa kaldırır.
//
// # Neden veritabanlarından ayrı bir paket
//
// Veritabanı örneği kalıcı bir varlık: adı, sürümü, veri dizini ve portu
// oturumdan bağımsız yaşıyor, "devbox db" ile tek tek yönetiliyor. Yan
// servisler ise projenin ömrüne bağlı — `devbox.yaml`'da "services: [redis]"
// yazıyor ve `devbox up` ile birlikte açılıp kapanıyorlar. Farklı yaşam
// döngüsü, farklı paket.
//
// # İkiliyi indirmiyoruz, buluyoruz
//
// Manifest yayın altyapısı henüz olmadığı için (bkz. internal/runtime)
// hiçbir ikili doğrulanarak indirilemiyor. Bu yüzden servisler PATH'te ya
// da DevBox'ın runtime dizininde aranıyor; bulunamazsa kurulum yolunu
// söyleyen açık bir hata veriliyor. Sessizce "servis yok" demek, uygulama
// Redis'e bağlanamayınca anlaşılması dakikalar süren bir hataya dönüşür.
package services

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// Kind, servis türü.
type Kind string

const (
	KindRedis       Kind = "redis"
	KindMeilisearch Kind = "meilisearch"
	KindMinIO       Kind = "minio"
)

// Kinds, desteklenen servisler.
func Kinds() []Kind { return []Kind{KindRedis, KindMeilisearch, KindMinIO} }

// Spec, devbox.yaml'daki bir servis girdisi ("redis", "redis@7").
type Spec struct {
	Kind Kind

	// Version, istenen sürüm. Boş olabilir.
	Version string
}

// ParseSpec, "redis@7" biçimindeki bir girdiyi çözer.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	name, version, _ := strings.Cut(s, "@")
	name = strings.ToLower(strings.TrimSpace(name))
	version = strings.TrimSpace(version)

	switch Kind(name) {
	case KindRedis, "valkey":
		return Spec{Kind: KindRedis, Version: version}, nil
	case KindMeilisearch, "meili":
		return Spec{Kind: KindMeilisearch, Version: version}, nil
	case KindMinIO, "s3":
		return Spec{Kind: KindMinIO, Version: version}, nil
	case "postgres", "postgresql", "mysql", "mariadb":
		return Spec{}, fmt.Errorf("services: %q bir veritabanı; \"devbox db create\" ile yönetiliyor", name)
	default:
		return Spec{}, fmt.Errorf("services: bilinmeyen servis %q (redis, meilisearch, minio)", name)
	}
}

// Service, çalışan tek bir yan servis.
type Service struct {
	Spec

	// Binary, bulunan çalıştırılabilir.
	Binary string

	// DataDir, servisin verisini yazacağı dizin.
	DataDir string

	// Port, ana port.
	Port int

	// ConsolePort, arayüzü olan servisler (MinIO) için ikinci port.
	ConsolePort int
}

// ServiceName, denetçide görünecek ad.
func (s *Service) ServiceName() string { return "servis-" + string(s.Kind) }

// Driver, bir servis türünün nasıl çalıştırılacağını bilir.
type Driver interface {
	Kind() Kind

	// Binaries, PATH'te aranacak adlar (ilk bulunan kullanılır).
	Binaries() []string

	// DefaultPort, tercih edilen port.
	DefaultPort() int

	// NeedsConsolePort, ikinci bir port isteyip istemediği.
	NeedsConsolePort() bool

	// ServiceConfig, denetçi yapılandırması.
	ServiceConfig(s *Service) supervisor.Config

	// Env, uygulamaya verilecek ortam değişkenleri.
	Env(s *Service) map[string]string

	// InstallHint, ikili bulunamazsa gösterilecek yol.
	InstallHint() string
}

// driverFor, türün sürücüsünü döner.
func driverFor(kind Kind) (Driver, error) {
	switch kind {
	case KindRedis:
		return &Redis{}, nil
	case KindMeilisearch:
		return &Meilisearch{}, nil
	case KindMinIO:
		return &MinIO{}, nil
	default:
		return nil, fmt.Errorf("services: sürücüsü olmayan servis %q", kind)
	}
}

// findBinary, ikiliyi önce verilen ek dizinlerde, sonra PATH'te arar.
func findBinary(names, extraDirs []string) (string, error) {
	for _, dir := range extraDirs {
		for _, name := range names {
			candidate := filepath.Join(dir, exeName(name))
			if info, err := osStat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("bulunamadı: %s", strings.Join(names, ", "))
}

// exeName, Windows'ta .exe ekler.
func exeName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		return name + ".exe"
	}
	return name
}

// envList, harita biçimindeki değişkenleri "K=V" dizisine çevirir.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
