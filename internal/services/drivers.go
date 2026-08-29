package services

import (
	"os"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// osStat, testte değiştirilebilsin diye.
var osStat = os.Stat

// Redis, Redis (ve API uyumlu Valkey) sürücüsü.
//
// Windows'ta resmî bir Redis derlemesi yok; Memurai ticari, Valkey'in
// Windows derlemesi ise topluluk işi. Bu yüzden ikili indirilmiyor,
// bulunuyor — kullanıcı hangisini kurduysa o çalışıyor.
type Redis struct{}

func (r *Redis) Kind() Kind             { return KindRedis }
func (r *Redis) Binaries() []string     { return []string{"redis-server", "valkey-server", "memurai"} }
func (r *Redis) DefaultPort() int       { return 6379 }
func (r *Redis) NeedsConsolePort() bool { return false }

func (r *Redis) ServiceConfig(s *Service) supervisor.Config {
	return supervisor.Config{
		Name: s.ServiceName(),
		Exec: s.Binary,
		Args: []string{
			"--port", itoa(s.Port),
			// Yalnız loopback: parolasız bir Redis'i ağa açmak, makinedeki
			// her şeyi okunur yazılır hâle getirmek demek.
			"--bind", "127.0.0.1",
			// Veri proje dizinine: iki proje birbirinin kuyruğunu
			// görmesin.
			"--dir", s.DataDir,
		},
		// Redis portu açtıktan sonra bu satırı yazıyor; TCP yerine günlük
		// satırı beklemek "bağlandım ama reddedildi" yarışını kapatıyor.
		Ready:         supervisor.LogReady{Substring: "Ready to accept connections"},
		StartTimeout:  30 * time.Second,
		StopTimeout:   10 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 15 * time.Second,
	}
}

func (r *Redis) Env(s *Service) map[string]string {
	return map[string]string{
		"REDIS_HOST": "127.0.0.1",
		"REDIS_PORT": itoa(s.Port),
		"REDIS_URL":  "redis://127.0.0.1:" + itoa(s.Port),
	}
}

func (r *Redis) InstallHint() string {
	return "Windows'ta Memurai ya da Valkey'in Windows derlemesi; Linux/macOS'ta paket yöneticisinden redis-server"
}

// Meilisearch, arama motoru sürücüsü.
type Meilisearch struct{}

func (m *Meilisearch) Kind() Kind             { return KindMeilisearch }
func (m *Meilisearch) Binaries() []string     { return []string{"meilisearch"} }
func (m *Meilisearch) DefaultPort() int       { return 7700 }
func (m *Meilisearch) NeedsConsolePort() bool { return false }

// DevMasterKey, geliştirme ortamı için sabit anahtar.
//
// Meilisearch üretim kipinde anahtar zorunlu tutuyor. Geliştirmede rastgele
// bir anahtar üretmek, her açılışta uygulamanın .env'ini güncellemeyi
// gerektirirdi; sabit ve yalnız loopback'te dinleyen bir anahtar burada
// doğru dengeyi kuruyor.
const DevMasterKey = "devbox-gelistirme-anahtari"

func (m *Meilisearch) ServiceConfig(s *Service) supervisor.Config {
	return supervisor.Config{
		Name: s.ServiceName(),
		Exec: s.Binary,
		Args: []string{
			"--http-addr", "127.0.0.1:" + itoa(s.Port),
			"--db-path", s.DataDir,
			"--env", "development",
			"--master-key", DevMasterKey,
			"--no-analytics",
		},
		Ready:         supervisor.TCPReady{Addr: "127.0.0.1:" + itoa(s.Port)},
		StartTimeout:  30 * time.Second,
		StopTimeout:   10 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 15 * time.Second,
	}
}

func (m *Meilisearch) Env(s *Service) map[string]string {
	return map[string]string{
		"MEILISEARCH_HOST": "http://127.0.0.1:" + itoa(s.Port),
		"MEILISEARCH_KEY":  DevMasterKey,
		"SCOUT_DRIVER":     "meilisearch",
	}
}

func (m *Meilisearch) InstallHint() string {
	return "https://www.meilisearch.com/docs/learn/self_hosted/install_meilisearch_locally"
}

// MinIO, S3 uyumlu nesne deposu sürücüsü.
type MinIO struct{}

func (m *MinIO) Kind() Kind             { return KindMinIO }
func (m *MinIO) Binaries() []string     { return []string{"minio"} }
func (m *MinIO) DefaultPort() int       { return 9000 }
func (m *MinIO) NeedsConsolePort() bool { return true }

// DevAccessKey / DevSecretKey, geliştirme ortamı kimlik bilgileri.
//
// Sabit: uygulamanın .env'i her açılışta değişmesin. Servis yalnız
// loopback'i dinliyor.
const (
	DevAccessKey = "devbox"
	DevSecretKey = "devbox-gelistirme-parolasi"
)

func (m *MinIO) ServiceConfig(s *Service) supervisor.Config {
	return supervisor.Config{
		Name: s.ServiceName(),
		Exec: s.Binary,
		Args: []string{
			"server", s.DataDir,
			"--address", "127.0.0.1:" + itoa(s.Port),
			"--console-address", "127.0.0.1:" + itoa(s.ConsolePort),
		},
		Env: []string{
			"MINIO_ROOT_USER=" + DevAccessKey,
			"MINIO_ROOT_PASSWORD=" + DevSecretKey,
			// Sürüm denetimi her açılışta ağa çıkıyor; çevrimdışı
			// çalışan bir geliştirme ortamında bunu istemiyoruz.
			"MINIO_UPDATE=off",
		},
		Ready:         supervisor.TCPReady{Addr: "127.0.0.1:" + itoa(s.Port)},
		StartTimeout:  30 * time.Second,
		StopTimeout:   15 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 15 * time.Second,
	}
}

func (m *MinIO) Env(s *Service) map[string]string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":           DevAccessKey,
		"AWS_SECRET_ACCESS_KEY":       DevSecretKey,
		"AWS_DEFAULT_REGION":          "us-east-1",
		"AWS_ENDPOINT":                "http://127.0.0.1:" + itoa(s.Port),
		"AWS_URL":                     "http://127.0.0.1:" + itoa(s.Port),
		"AWS_USE_PATH_STYLE_ENDPOINT": "true",
		"MINIO_CONSOLE":               "http://127.0.0.1:" + itoa(s.ConsolePort),
	}
}

func (m *MinIO) InstallHint() string { return "https://min.io/docs/minio/windows/index.html" }
