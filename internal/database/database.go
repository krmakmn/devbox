// Package database, veritabanı örneklerini kurar ve yönetir.
//
// Laragon'da tek bir MySQL servisi vardır: tüm projeler onu paylaşır, sürüm
// değiştirmek herkesi etkiler ve bir projeyi bozan geçiş (migration) diğerini
// de bozar. Buradaki birim **örnek** (instance): her birinin kendi sürümü,
// kendi veri dizini ve kendi portu var. PostgreSQL 16 ile 17 aynı anda
// ayakta durabilir.
//
// # Hazır olma denetimi neden günlükten
//
// MySQL ve MariaDB portu, bağlantı kabul etmeye hazır olmadan önce açıyor:
// TCP bağlantısı kuruluyor ama sunucu "Server is not ready" ile reddediyor.
// Bu yüzden hazır olma ölçütü olarak TCP değil, sunucunun kendi günlüğüne
// yazdığı satır kullanılıyor. PostgreSQL'de de aynı yol izleniyor;
// pg_isready ayrı bir süreç çalıştırmayı gerektiriyor ve günlük satırı kadar
// kesin bilgi vermiyor.
package database

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// Engine, veritabanı motoru.
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
	EngineMariaDB  Engine = "mariadb"
)

// Engines, desteklenen motorlar.
func Engines() []Engine { return []Engine{EnginePostgres, EngineMySQL, EngineMariaDB} }

// ParseEngine, metinden motoru çözer.
func ParseEngine(s string) (Engine, error) {
	switch Engine(strings.ToLower(strings.TrimSpace(s))) {
	case EnginePostgres, "postgresql", "pg":
		return EnginePostgres, nil
	case EngineMySQL:
		return EngineMySQL, nil
	case EngineMariaDB:
		return EngineMariaDB, nil
	default:
		return "", fmt.Errorf("database: bilinmeyen motor %q (postgres, mysql ya da mariadb)", s)
	}
}

// Binaries, bir motorun araçlarının yolları.
type Binaries struct {
	// Server, sunucu süreci (postgres, mysqld, mariadbd).
	Server string

	// Init, veri dizinini hazırlayan araç. MySQL'de sunucunun kendisi.
	Init string

	// Dump, yedek alan araç (pg_dumpall, mysqldump, mariadb-dump).
	Dump string

	// Client, SQL çalıştıran istemci (psql, mysql, mariadb).
	Client string
}

// Instance, tek bir veritabanı örneği.
type Instance struct {
	// Name, örneğin adı. Dizin adı olarak da kullanılır.
	Name string `json:"name"`

	// Engine, motoru.
	Engine Engine `json:"engine"`

	// Version, insan tarafından okunabilir sürüm (üstveride tutulur).
	Version string `json:"version,omitempty"`

	// Port, dinlenen TCP portu.
	Port int `json:"port"`

	// DataDir, veri dizini.
	DataDir string `json:"dataDir"`

	// Superuser, yönetici kullanıcı adı.
	Superuser string `json:"superuser"`

	// Binaries, kullanılan araçların yolları. Sürüm bilgisi burada
	// saklanıyor: aynı makinede iki PostgreSQL sürümü varsa her örnek
	// kendi ikilisini hatırlamalı.
	Binaries Binaries `json:"binaries"`

	// CreatedAt, oluşturulma zamanı.
	CreatedAt time.Time `json:"createdAt"`
}

// Addr, örneğin bağlantı adresi.
func (i *Instance) Addr() string { return fmt.Sprintf("127.0.0.1:%d", i.Port) }

// ServiceName, denetçideki servis adı.
func (i *Instance) ServiceName() string { return "db-" + i.Name }

// Driver, bir motorun DevBox'a bakan yüzü.
type Driver interface {
	// Engine, sürücünün motoru.
	Engine() Engine

	// DefaultSuperuser, varsayılan yönetici kullanıcı adı.
	DefaultSuperuser() string

	// Initialize, veri dizinini hazırlar.
	Initialize(inst *Instance) *exec.Cmd

	// ServiceConfig, sunucuyu çalıştıracak servis tanımını üretir.
	ServiceConfig(inst *Instance) supervisor.Config

	// DumpCommand, tüm veritabanlarını dosyaya yazan komutu üretir.
	DumpCommand(inst *Instance, dest string) *exec.Cmd

	// RestoreCommand, dosyadan geri yükleyen komutu üretir.
	RestoreCommand(inst *Instance, src string) *exec.Cmd

	// RestoreReadsStdin, geri yükleme komutunun dosyayı argümanla değil
	// standart girdiden okuduğunu söyler (mysql/mariadb istemcisi böyle).
	RestoreReadsStdin() bool

	// ReadyLine, sunucunun hazır olduğunu bildiren günlük satırı.
	ReadyLine() string
}

// DriverFor, motora karşılık gelen sürücüyü döner.
func DriverFor(engine Engine) (Driver, error) {
	switch engine {
	case EnginePostgres:
		return &Postgres{}, nil
	case EngineMySQL:
		return &MySQL{Variant: EngineMySQL}, nil
	case EngineMariaDB:
		return &MySQL{Variant: EngineMariaDB}, nil
	default:
		return nil, fmt.Errorf("database: %q için sürücü yok", engine)
	}
}

// Locate, motorun araçlarını bulur.
//
// hint verilmişse önce onun bin/ dizinine bakılır (DevBox'ın kurduğu
// runtime), sonra PATH'e. Windows'ta .exe uzantısı exec.LookPath tarafından
// eklenir.
func Locate(engine Engine, hint string) (Binaries, error) {
	var b Binaries
	var names map[string]*string

	switch engine {
	case EnginePostgres:
		names = map[string]*string{
			"postgres":   &b.Server,
			"initdb":     &b.Init,
			"pg_dumpall": &b.Dump,
			"psql":       &b.Client,
		}
	case EngineMySQL:
		names = map[string]*string{
			"mysqld":    &b.Server,
			"mysqldump": &b.Dump,
			"mysql":     &b.Client,
		}
	case EngineMariaDB:
		names = map[string]*string{
			"mariadbd":           &b.Server,
			"mariadb-install-db": &b.Init,
			"mariadb-dump":       &b.Dump,
			"mariadb":            &b.Client,
		}
	default:
		return b, fmt.Errorf("database: %q için araç listesi yok", engine)
	}

	var missing []string
	for name, target := range names {
		path, err := findTool(name, hint)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		*target = path
	}
	if len(missing) > 0 {
		return b, fmt.Errorf("database: %s araçları bulunamadı: %s\n"+
			"DevBox ile kurun (devbox runtime install %s) ya da -bin ile kurulum dizinini verin",
			engine, strings.Join(missing, ", "), engine)
	}

	// MySQL'de veri dizinini sunucunun kendisi hazırlıyor.
	if engine == EngineMySQL {
		b.Init = b.Server
	}
	return b, nil
}

func findTool(name, hint string) (string, error) {
	if hint != "" {
		for _, dir := range []string{filepath.Join(hint, "bin"), hint} {
			candidate := filepath.Join(dir, name)
			if runtime.GOOS == "windows" {
				candidate += ".exe"
			}
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	return exec.LookPath(name)
}

// checkNotPrivileged, ayrıcalıklı kullanıcı denetimi yapar.
//
// Üç motor da root olarak çalışmayı reddediyor: PostgreSQL'in initdb'si
// açıkça durur, MySQL ve MariaDB sunucuları "Please read Security section
// of the manual" diyerek çıkar. Sebebi ortak: veri dizininin sahibi,
// sunucuyu çalıştıracak kullanıcı olmalı ve root olarak açılan bir
// veritabanı, ele geçirildiğinde makinenin tamamını verir.
//
// Bunu önceden söylüyoruz çünkü motorların kendi mesajları bağlamdan kopuk
// geliyor: kullanıcı komutu neden yükselttiğini hatırlamayabilir.
//
// Windows'ta böyle bir kısıt yok; orada denetim yapılmıyor.
func checkNotPrivileged(engine Engine) error {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		return nil
	}
	return fmt.Errorf("database: %s root olarak çalıştırılamaz; "+
		"komutu normal kullanıcıyla çalıştırın", engine)
}
