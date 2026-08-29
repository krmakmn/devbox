package database

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/supervisor"
)

// --- birim testleri -------------------------------------------------------

func TestParseEngine(t *testing.T) {
	ok := map[string]Engine{
		"postgres": EnginePostgres, "postgresql": EnginePostgres, "PG": EnginePostgres,
		"mysql": EngineMySQL, "MariaDB": EngineMariaDB, " mariadb ": EngineMariaDB,
	}
	for in, want := range ok {
		got, err := ParseEngine(in)
		if err != nil || got != want {
			t.Errorf("ParseEngine(%q) = %q, %v; beklenen %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "oracle", "sqlite", "mssql"} {
		if _, err := ParseEngine(in); err == nil {
			t.Errorf("ParseEngine(%q) hata vermedi", in)
		}
	}
}

func TestInstanceNameValidation(t *testing.T) {
	m := NewManager(t.TempDir(), nil, nil, nil)
	for _, name := range []string{"", "a b", "../kacti", "a/b", strings.Repeat("x", 65)} {
		if _, err := m.Create(context.Background(), Spec{Name: name, Engine: EnginePostgres}); err == nil {
			t.Errorf("geçersiz örnek adı kabul edildi: %q", name)
		}
	}
}

// Etiket dosya adına giriyor; yol ayracı ya da ".." içeren bir etiket
// yedeği istenmeyen bir yere yazmak demek.
func TestSnapshotTagValidation(t *testing.T) {
	for _, tag := range []string{"", "../kacti", "a/b", `a\b`, strings.Repeat("x", 129)} {
		if validTag(tag) {
			t.Errorf("geçersiz etiket kabul edildi: %q", tag)
		}
	}
	for _, tag := range []string{"gecis-oncesi", "2026-08-29", "v1_0"} {
		if !validTag(tag) {
			t.Errorf("geçerli etiket reddedildi: %q", tag)
		}
	}
}

func TestDefaultPorts(t *testing.T) {
	if defaultPort(EnginePostgres) != 5432 {
		t.Error("postgres varsayılan portu yanlış")
	}
	if defaultPort(EngineMySQL) != 3306 || defaultPort(EngineMariaDB) != 3306 {
		t.Error("mysql/mariadb varsayılan portu yanlış")
	}
}

func TestLocateReportsMissingTools(t *testing.T) {
	_, err := Locate(EnginePostgres, filepath.Join(t.TempDir(), "yok"))
	if err == nil {
		// PATH'te postgres varsa bu test anlamsız.
		if _, lookErr := exec.LookPath("initdb"); lookErr == nil {
			t.Skip("initdb PATH'te var")
		}
		t.Fatal("eksik araçlar bildirilmedi")
	}
	if !strings.Contains(err.Error(), "devbox runtime install") {
		t.Errorf("hata çözüm önermiyor: %v", err)
	}
}

// MySQL ve MariaDB portu, bağlantı kabul etmeye hazır olmadan önce açıyor;
// TCP denetimi yanıltıcı. Hazır olma ölçütü günlük satırı olmalı.
func TestReadyChecksUseLogNotTCP(t *testing.T) {
	for _, engine := range Engines() {
		driver, err := DriverFor(engine)
		if err != nil {
			t.Fatal(err)
		}
		inst := &Instance{Name: "x", Engine: engine, Port: 1234, DataDir: "/veri"}
		cfg := driver.ServiceConfig(inst)

		ready, ok := cfg.Ready.(supervisor.LogReady)
		if !ok {
			t.Errorf("%s: hazır olma ölçütü LogReady değil (%T)", engine, cfg.Ready)
			continue
		}
		if ready.Substring != driver.ReadyLine() {
			t.Errorf("%s: beklenen günlük satırı %q, ölçütte %q", engine, driver.ReadyLine(), ready.Substring)
		}
	}
}

// Parolasız bir geliştirme veritabanını ağa açmak kabul edilemez.
func TestServersBindToLoopbackOnly(t *testing.T) {
	for _, engine := range Engines() {
		driver, _ := DriverFor(engine)
		inst := &Instance{Name: "x", Engine: engine, Port: 1234, DataDir: "/veri", Superuser: "u"}
		args := strings.Join(driver.ServiceConfig(inst).Args, " ")
		if !strings.Contains(args, "127.0.0.1") {
			t.Errorf("%s loopback'e bağlanmıyor: %s", engine, args)
		}
	}
}

// --- gerçek motorlara karşı testler ---------------------------------------

// engineAvailable, motorun araçları kurulu mu diye bakar.
func engineAvailable(t *testing.T, engine Engine) Binaries {
	t.Helper()
	bins, err := Locate(engine, postgresBinDir(engine))
	if err != nil {
		t.Skipf("%s kurulu değil; gerçek motor testi atlanıyor", engine)
	}
	return bins
}

// postgresBinDir, dağıtımların PostgreSQL'i sürüm dizinine koyması yüzünden
// gereken ipucu.
func postgresBinDir(engine Engine) string {
	if engine != EnginePostgres {
		return ""
	}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	if len(matches) > 0 {
		return filepath.Dir(matches[len(matches)-1])
	}
	return ""
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	sup, err := supervisor.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sup.Close() })
	return NewManager(t.TempDir(), sup, ports.New("127.0.0.1"), nil)
}

// createAndStart, örneği kurup başlatır.
func createAndStart(t *testing.T, m *Manager, name string, engine Engine) *Instance {
	t.Helper()
	bins := engineAvailable(t, engine)
	_ = bins

	if err := checkNotPrivileged(engine); err != nil {
		t.Skipf("%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	inst, err := m.Create(ctx, Spec{Name: name, Engine: engine, BinDir: postgresBinDir(engine)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Start(ctx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { m.Stop(name) })
	return inst
}

// sql, örnekte SQL çalıştırır ve çıktıyı döner.
func sql(t *testing.T, inst *Instance, statement string) string {
	t.Helper()
	var cmd *exec.Cmd
	switch inst.Engine {
	case EnginePostgres:
		cmd = exec.Command(inst.Binaries.Client,
			"-h", "127.0.0.1", "-p", strconv.Itoa(inst.Port), "-U", inst.Superuser,
			"-d", "postgres", "-t", "-A", "-c", statement)
	default:
		cmd = exec.Command(inst.Binaries.Client,
			"-h", "127.0.0.1", "-P", strconv.Itoa(inst.Port), "-u", inst.Superuser,
			"-N", "-B", "-e", statement)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQL başarısız (%s): %v\n%s", statement, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPostgresLifecycle(t *testing.T) {
	m := newManager(t)
	inst := createAndStart(t, m, "pgtest", EnginePostgres)

	if got := sql(t, inst, "select 1"); got != "1" {
		t.Errorf("sorgu sonucu %q", got)
	}
	if inst.Port == 0 {
		t.Error("port atanmadı")
	}

	// Üstveri diskten okunabilmeli: DevBox yeniden başlasa da örneği
	// bulmalı.
	reloaded, err := m.Get("pgtest")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.Port != inst.Port || reloaded.Engine != EnginePostgres {
		t.Errorf("üstveri eşleşmiyor: %+v", reloaded)
	}

	list, err := m.List()
	if err != nil || len(list) != 1 {
		t.Errorf("List = %v, %v", list, err)
	}
}

// Yol haritasının kabul kriteri: aynı motorun iki örneği aynı anda ayakta
// ve verileri birbirinden yalıtık.
func TestTwoPostgresInstancesSideBySide(t *testing.T) {
	m := newManager(t)
	first := createAndStart(t, m, "birinci", EnginePostgres)
	second := createAndStart(t, m, "ikinci", EnginePostgres)

	if first.Port == second.Port {
		t.Fatalf("iki örnek aynı portu kullanıyor: %d", first.Port)
	}
	if first.DataDir == second.DataDir {
		t.Fatal("iki örnek aynı veri dizinini kullanıyor")
	}

	sql(t, first, "create database yalnizca_birincide")
	got := sql(t, second, "select count(*) from pg_database where datname='yalnizca_birincide'")
	if got != "0" {
		t.Errorf("ikinci örnek birincinin veritabanını görüyor: %q", got)
	}
}

func TestPostgresSnapshotAndRestore(t *testing.T) {
	m := newManager(t)
	inst := createAndStart(t, m, "yedektest", EnginePostgres)
	ctx := context.Background()

	sql(t, inst, "create database magaza")
	sqlOn(t, inst, "magaza", "create table urun(id int primary key, ad text)")
	sqlOn(t, inst, "magaza", "insert into urun values (1,'kalem'),(2,'defter')")

	path, err := m.Snapshot(ctx, "yedektest", "gecis-oncesi")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("anlık görüntü dizini yok: %v", err)
	}

	// Yıkıcı değişiklik.
	sqlOn(t, inst, "magaza", "delete from urun where id=2")
	if got := sqlOn(t, inst, "magaza", "select count(*) from urun"); got != "1" {
		t.Fatalf("silme uygulanmadı: %q", got)
	}

	if err := m.Restore(ctx, "yedektest", "gecis-oncesi"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := sqlOn(t, inst, "magaza", "select count(*) from urun"); got != "2" {
		t.Errorf("geri yüklemeden sonra satır sayısı %q, beklenen 2 — veri kaybı", got)
	}
	if got := sqlOn(t, inst, "magaza", "select ad from urun where id=2"); got != "defter" {
		t.Errorf("geri yüklenen veri yanlış: %q", got)
	}

	snaps, err := m.Snapshots("yedektest")
	if err != nil || len(snaps) != 1 || snaps[0] != "gecis-oncesi" {
		t.Errorf("anlık görüntü listesi = %v, %v", snaps, err)
	}
}

// Anlık görüntü veri dizinini kopyaladığı için roller, ayarlar ve diziler de
// birlikte geliyor — SQL dökümünün kaçırdığı ya da geri yüklerken
// çakıştırdığı şeyler.
func TestPostgresSnapshotRestoresRolesToo(t *testing.T) {
	m := newManager(t)
	inst := createAndStart(t, m, "roltest", EnginePostgres)
	ctx := context.Background()

	sql(t, inst, "create role uygulama login")
	if _, err := m.Snapshot(ctx, "roltest", "rolluyken"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	sql(t, inst, "drop role uygulama")
	if got := sql(t, inst, "select count(*) from pg_roles where rolname='uygulama'"); got != "0" {
		t.Fatalf("rol silinmedi: %q", got)
	}

	if err := m.Restore(ctx, "roltest", "rolluyken"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := sql(t, inst, "select count(*) from pg_roles where rolname='uygulama'"); got != "1" {
		t.Errorf("rol geri gelmedi: %q — SQL dökümünün kaçırdığı durum", got)
	}
}

// Dışa aktarma, anlık görüntüden farklı bir iş yapıyor: birebir değil ama
// sürümler ve makineler arası taşınabilir.
func TestPostgresExport(t *testing.T) {
	m := newManager(t)
	inst := createAndStart(t, m, "disaaktar", EnginePostgres)
	ctx := context.Background()

	sql(t, inst, "create database magaza")
	sqlOn(t, inst, "magaza", "create table urun(id int); insert into urun values (7)")

	dest := filepath.Join(t.TempDir(), "disa.sql")
	if err := m.Export(ctx, "disaaktar", dest); err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CREATE DATABASE magaza") {
		t.Error("dışa aktarılan dosya veritabanını içermiyor")
	}
	if !strings.Contains(string(data), "urun") {
		t.Error("dışa aktarılan dosya tabloyu içermiyor")
	}
}

func TestMariaDBLifecycleAndSnapshot(t *testing.T) {
	m := newManager(t)
	inst := createAndStart(t, m, "mdbtest", EngineMariaDB)
	ctx := context.Background()

	sql(t, inst, "create database magaza")
	sql(t, inst, "create table magaza.urun(id int primary key, ad varchar(50))")
	sql(t, inst, "insert into magaza.urun values (1,'kalem'),(2,'defter')")

	if _, err := m.Snapshot(ctx, "mdbtest", "gecis-oncesi"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	sql(t, inst, "delete from magaza.urun where id=2")
	if got := sql(t, inst, "select count(*) from magaza.urun"); got != "1" {
		t.Fatalf("silme uygulanmadı: %q", got)
	}

	if err := m.Restore(ctx, "mdbtest", "gecis-oncesi"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := sql(t, inst, "select count(*) from magaza.urun"); got != "2" {
		t.Errorf("geri yüklemeden sonra satır sayısı %q, beklenen 2 — veri kaybı", got)
	}
}

// Farklı motorların aynı anda çalışabilmesi, Laragon'un tek MySQL servisiyle
// arasındaki en somut fark.
func TestPostgresAndMariaDBTogether(t *testing.T) {
	m := newManager(t)
	pg := createAndStart(t, m, "pg", EnginePostgres)
	mdb := createAndStart(t, m, "mdb", EngineMariaDB)

	if pg.Port == mdb.Port {
		t.Fatal("iki motor aynı portu kullanıyor")
	}
	if got := sql(t, pg, "select 1"); got != "1" {
		t.Errorf("postgres yanıtı %q", got)
	}
	if got := sql(t, mdb, "select 1"); got != "1" {
		t.Errorf("mariadb yanıtı %q", got)
	}
}

func TestRemoveKeepsSnapshots(t *testing.T) {
	m := newManager(t)
	createAndStart(t, m, "silinecek", EnginePostgres)
	ctx := context.Background()

	if _, err := m.Snapshot(ctx, "silinecek", "son"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := m.Remove("silinecek"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := m.Get("silinecek"); err == nil {
		t.Error("silinen örnek hâlâ bulunuyor")
	}

	// Örneği silmek "verilerden tümüyle vazgeçtim" demek olmayabilir;
	// yedeği yanlışlıkla silmek geri alınamaz.
	snaps, err := m.Snapshots("silinecek")
	if err != nil || len(snaps) != 1 {
		t.Errorf("örnek silinince anlık görüntüler de silindi: %v, %v", snaps, err)
	}
}

func TestDuplicateInstanceRejected(t *testing.T) {
	m := newManager(t)
	createAndStart(t, m, "tek", EnginePostgres)

	_, err := m.Create(context.Background(), Spec{
		Name: "tek", Engine: EnginePostgres, BinDir: postgresBinDir(EnginePostgres),
	})
	if err == nil {
		t.Fatal("aynı adla ikinci örnek kabul edildi")
	}
	if !strings.Contains(err.Error(), "zaten var") {
		t.Errorf("hata = %v", err)
	}
}

// sqlOn, belirli bir veritabanında SQL çalıştırır (yalnız PostgreSQL).
func sqlOn(t *testing.T, inst *Instance, database, statement string) string {
	t.Helper()
	cmd := exec.Command(inst.Binaries.Client,
		"-h", "127.0.0.1", "-p", strconv.Itoa(inst.Port), "-U", inst.Superuser,
		"-d", database, "-t", "-A", "-c", statement)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQL başarısız (%s): %v\n%s", statement, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Port ataması kalıcı olmalı: tahsis edici süreç ömrüyle sınırlı, ama
// örnekler diskte kalıyor. İki ayrı "devbox db create" çağrısı aynı portu
// verirse ikinci örnek hiç başlatılamaz.
func TestPortsAreNotReusedAcrossManagers(t *testing.T) {
	engineAvailable(t, EnginePostgres)
	if err := checkNotPrivileged(EnginePostgres); err != nil {
		t.Skipf("%v", err)
	}

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Her çağrı için taze bir yönetici ve taze bir tahsis edici: ayrı
	// süreçlerde çalıştırmanın karşılığı.
	newFresh := func() *Manager {
		sup, err := supervisor.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sup.Close() })
		return NewManager(root, sup, ports.New("127.0.0.1"), nil)
	}

	first, err := newFresh().Create(ctx, Spec{
		Name: "birinci", Engine: EnginePostgres, BinDir: postgresBinDir(EnginePostgres),
	})
	if err != nil {
		t.Fatalf("birinci Create: %v", err)
	}
	second, err := newFresh().Create(ctx, Spec{
		Name: "ikinci", Engine: EnginePostgres, BinDir: postgresBinDir(EnginePostgres),
	})
	if err != nil {
		t.Fatalf("ikinci Create: %v", err)
	}

	if first.Port == second.Port {
		t.Errorf("iki örnek aynı portu aldı: %d", first.Port)
	}
}
