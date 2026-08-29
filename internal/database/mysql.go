package database

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// MySQL, MySQL ve MariaDB sürücüsü.
//
// İki motor komut satırı düzeyinde büyük ölçüde aynı; ayrıldıkları yer veri
// dizinini hazırlama biçimi. MySQL bunu sunucunun kendisiyle
// (--initialize-insecure) yapıyor, MariaDB ayrı bir araçla
// (mariadb-install-db).
type MySQL struct {
	// Variant, EngineMySQL ya da EngineMariaDB.
	Variant Engine
}

func (m *MySQL) Engine() Engine           { return m.Variant }
func (m *MySQL) DefaultSuperuser() string { return "root" }

// Initialize, veri dizinini hazırlar.
//
// Parolasız root: yerel geliştirme veritabanı yalnız loopback'i dinliyor.
// Ağa açık bir kurulumda kabul edilemez.
func (m *MySQL) Initialize(inst *Instance) *exec.Cmd {
	if m.Variant == EngineMariaDB {
		return exec.Command(inst.Binaries.Init,
			"--datadir="+inst.DataDir,
			"--auth-root-authentication-method=normal",
			// Örnek "test" veritabanı ve anonim kullanıcılar oluşturulmasın.
			"--skip-test-db",
		)
	}
	return exec.Command(inst.Binaries.Init,
		"--initialize-insecure",
		"--datadir="+inst.DataDir,
	)
}

func (m *MySQL) ServiceConfig(inst *Instance) supervisor.Config {
	args := []string{
		"--datadir=" + inst.DataDir,
		"--port=" + strconv.Itoa(inst.Port),
		// Yalnız loopback: parolasız bir veritabanını ağa açmak kabul
		// edilemez.
		"--bind-address=127.0.0.1",
		"--pid-file=" + filepath.Join(inst.DataDir, "mysqld.pid"),
		// Soket yolu veri dizinine: derleme varsayılanı
		// (/run/mysqld/mysqld.sock) çoğu ortamda yok ya da yazılabilir
		// değil ve sunucu "Bind on unix socket" ile açılışta ölüyor.
		// Ayrıca iki örneğin aynı sokete çakışmasını da önlüyor.
		"--socket=" + filepath.Join(inst.DataDir, "mysqld.sock"),
	}
	return supervisor.Config{
		Name:          inst.ServiceName(),
		Exec:          inst.Binaries.Server,
		Args:          args,
		Ready:         supervisor.LogReady{Substring: m.ReadyLine()},
		StartTimeout:  90 * time.Second,
		StopTimeout:   30 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 15 * time.Second,
	}
}

// ReadyLine, sunucunun hazır olduğunu bildiren günlük satırı.
//
// TCP portunun açılması yetmiyor: MySQL ve MariaDB portu, bağlantı kabul
// etmeye hazır olmadan önce açıyor ve bu aralıkta bağlanan istemciye
// "Server is not ready" diyor. Günlük satırı tek kesin işaret.
func (m *MySQL) ReadyLine() string { return "ready for connections" }

func (m *MySQL) DumpCommand(inst *Instance, dest string) *exec.Cmd {
	cmd := exec.Command(inst.Binaries.Dump,
		"-h", "127.0.0.1",
		"-P", strconv.Itoa(inst.Port),
		"-u", inst.Superuser,
		"--all-databases",
		// Tutarlı bir anlık görüntü: yedek alınırken yazılan veriler
		// dosyanın yarısında görünüp yarısında görünmesin.
		"--single-transaction",
		"--result-file="+dest,
	)
	return cmd
}

func (m *MySQL) RestoreCommand(inst *Instance, src string) *exec.Cmd {
	// mysql/mariadb istemcisi dosyayı argümanla almıyor; standart girdiden
	// okuyor. Dosyayı açıp Stdin'e bağlamak çağıranın işi.
	return exec.Command(inst.Binaries.Client,
		"-h", "127.0.0.1",
		"-P", strconv.Itoa(inst.Port),
		"-u", inst.Superuser,
	)
}

// RestoreReadsStdin, mysql/mariadb istemcisi dosyayı argümanla almadığı,
// standart girdiden okuduğu için true.
func (m *MySQL) RestoreReadsStdin() bool { return true }
