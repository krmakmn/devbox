package database

import (
	"os/exec"
	"strconv"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// Postgres, PostgreSQL sürücüsü.
type Postgres struct{}

func (p *Postgres) Engine() Engine           { return EnginePostgres }
func (p *Postgres) DefaultSuperuser() string { return "postgres" }

// Initialize, veri dizinini hazırlar.
//
// --auth=trust: yerel geliştirme veritabanı yalnız loopback'i dinliyor ve
// parola sormuyor. Ağa açık bir kurulumda kabul edilemez; burada kasıtlı,
// çünkü her projeye parola yönetmek ettirmek DevBox'ın çözmeye çalıştığı
// sürtünmenin ta kendisi.
//
// --no-locale: yerel ayarların kurulu olmaması Windows'ta ve sade
// konteynerlerde initdb'yi düşürüyor; geliştirme veritabanı için dil
// sıralamasının önemi yok.
func (p *Postgres) Initialize(inst *Instance) *exec.Cmd {
	return exec.Command(inst.Binaries.Init,
		"-D", inst.DataDir,
		"-U", inst.Superuser,
		"--auth=trust",
		"--encoding=UTF8",
		"--no-locale",
	)
}

func (p *Postgres) ServiceConfig(inst *Instance) supervisor.Config {
	return supervisor.Config{
		Name: inst.ServiceName(),
		Exec: inst.Binaries.Server,
		Args: []string{
			"-D", inst.DataDir,
			"-p", strconv.Itoa(inst.Port),
			// Yalnız loopback: parolasız bir veritabanını ağa açmak
			// kabul edilemez.
			"-h", "127.0.0.1",
			// Unix soketi veri dizinine: /tmp'de başka bir PostgreSQL
			// örneğiyle çakışmasın. Windows'ta yok sayılıyor.
			"-k", inst.DataDir,
		},
		Ready:        supervisor.LogReady{Substring: p.ReadyLine()},
		StartTimeout: 60 * time.Second,
		// Veritabanının düzgün kapanması gerekiyor; nazik durdurma için
		// süre tanıyoruz.
		StopTimeout:   30 * time.Second,
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 15 * time.Second,
	}
}

func (p *Postgres) ReadyLine() string { return "database system is ready to accept connections" }

// DumpCommand, tüm veritabanlarını ve rolleri tek dosyaya yazar.
//
// pg_dump yerine pg_dumpall: rolleri ve veritabanı düzeyindeki ayarları da
// alıyor. Anlık görüntünün amacı "geçişten önceki hâle dönmek"; eksik rol
// bırakmak o vaadi bozar.
func (p *Postgres) DumpCommand(inst *Instance, dest string) *exec.Cmd {
	return exec.Command(inst.Binaries.Dump,
		"-h", "127.0.0.1",
		"-p", strconv.Itoa(inst.Port),
		"-U", inst.Superuser,
		"-f", dest,
	)
}

func (p *Postgres) RestoreCommand(inst *Instance, src string) *exec.Cmd {
	return exec.Command(inst.Binaries.Client,
		"-h", "127.0.0.1",
		"-p", strconv.Itoa(inst.Port),
		"-U", inst.Superuser,
		"-d", "postgres",
		// Hata görürse dursun: yarım geri yükleme, geri yüklenmemiş olmaktan
		// daha tehlikeli çünkü kullanıcı başarılı sandığı bir durumla
		// çalışmaya devam eder.
		"-v", "ON_ERROR_STOP=1",
		"-f", src,
	)
}

// RestoreReadsStdin, psql dosyayı -f ile aldığı için false.
func (p *Postgres) RestoreReadsStdin() bool { return false }
