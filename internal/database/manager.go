package database

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/supervisor"
)

// Manager, veritabanı örneklerini diskte ve denetçide yönetir.
//
// Dizin düzeni:
//
//	root/<ad>/           veri dizini
//	root/<ad>.json       üstveri
//	root/.snapshots/<ad>/<etiket>.sql
type Manager struct {
	root   string
	sup    *supervisor.Supervisor
	alloc  *ports.Allocator
	logger *slog.Logger
}

// NewManager, verilen kök dizinde bir yönetici oluşturur.
func NewManager(root string, sup *supervisor.Supervisor, alloc *ports.Allocator, logger *slog.Logger) *Manager {
	if alloc == nil {
		alloc = ports.New("127.0.0.1")
	}
	return &Manager{root: root, sup: sup, alloc: alloc, logger: logger}
}

// Root, yöneticinin kök dizini.
func (m *Manager) Root() string { return m.root }

func (m *Manager) dataDir(name string) string  { return filepath.Join(m.root, name) }
func (m *Manager) metaPath(name string) string { return filepath.Join(m.root, name+".json") }
func (m *Manager) snapshotDir(name string) string {
	return filepath.Join(m.root, ".snapshots", name)
}

// Spec, yeni bir örneğin isteği.
type Spec struct {
	// Name, örneğin adı.
	Name string

	// Engine, motoru.
	Engine Engine

	// Version, üstveride tutulacak sürüm etiketi.
	Version string

	// Port, istenen port. 0 ise motorun varsayılanından başlanarak boş
	// port aranır.
	Port int

	// BinDir, motorun kurulum dizini. Boşsa PATH'te aranır.
	BinDir string
}

// defaultPort, motorun alışılmış portu. Aynı motorun ikinci örneği bir
// sonraki boş porta düşüyor; böylece adresler tahmin edilebilir kalıyor.
func defaultPort(engine Engine) int {
	switch engine {
	case EnginePostgres:
		return 5432
	case EngineMySQL, EngineMariaDB:
		return 3306
	default:
		return 0
	}
}

// Create, yeni bir örnek kurar: veri dizinini hazırlar ve üstveriyi yazar.
//
// Sunucuyu başlatmaz; bunun için Start kullanılır.
func (m *Manager) Create(ctx context.Context, spec Spec) (*Instance, error) {
	if !validInstanceName(spec.Name) {
		return nil, fmt.Errorf("database: geçersiz örnek adı %q (harf, rakam, tire, alt çizgi)", spec.Name)
	}
	if existing, err := m.Get(spec.Name); err == nil {
		return existing, fmt.Errorf("database: %q adında bir örnek zaten var (%s)", spec.Name, existing.Engine)
	}
	if err := checkNotPrivileged(spec.Engine); err != nil {
		return nil, err
	}

	driver, err := DriverFor(spec.Engine)
	if err != nil {
		return nil, err
	}
	bins, err := Locate(spec.Engine, spec.BinDir)
	if err != nil {
		return nil, err
	}

	// Var olan örneklerin portlarını tahsis ediciye bildiriyoruz. Port
	// ataması kalıcı: örnek durmuş olsa bile portu başkasına verilmemeli,
	// yoksa iki örnek aynı adrese sahip olur ve ikincisi başlatılamaz.
	// Tahsis edici süreç ömrüyle sınırlı olduğu için her çağrıda yeniden
	// bildirmek gerekiyor.
	m.reserveKnownPorts()

	preferred := spec.Port
	if preferred == 0 {
		preferred = defaultPort(spec.Engine)
	}
	port, err := m.alloc.Allocate(preferred)
	if err != nil {
		return nil, err
	}

	inst := &Instance{
		Name:      spec.Name,
		Engine:    spec.Engine,
		Version:   spec.Version,
		Port:      port,
		DataDir:   m.dataDir(spec.Name),
		Superuser: driver.DefaultSuperuser(),
		Binaries:  bins,
		CreatedAt: time.Now().UTC(),
	}

	// Veri dizini hazırlanırken yarıda kalırsa geriye "kurulu görünen ama
	// bozuk" bir örnek bırakmıyoruz.
	if err := os.MkdirAll(filepath.Dir(inst.DataDir), 0o755); err != nil {
		return nil, err
	}
	if err := runCommand(ctx, driver.Initialize(inst), 5*time.Minute); err != nil {
		os.RemoveAll(inst.DataDir)
		m.alloc.Release(port)
		return nil, fmt.Errorf("database: %s veri dizini hazırlanamadı: %w", spec.Engine, err)
	}

	if err := m.writeMeta(inst); err != nil {
		os.RemoveAll(inst.DataDir)
		m.alloc.Release(port)
		return nil, err
	}
	return inst, nil
}

// Start, örneğin sunucusunu denetçi altında başlatır ve hazır olmasını
// bekler.
func (m *Manager) Start(ctx context.Context, name string) (*Instance, error) {
	inst, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if err := checkNotPrivileged(inst.Engine); err != nil {
		return nil, err
	}
	if m.sup == nil {
		return nil, fmt.Errorf("database: denetçi yok")
	}

	driver, err := DriverFor(inst.Engine)
	if err != nil {
		return nil, err
	}

	svc, ok := m.sup.Get(inst.ServiceName())
	if !ok {
		svc, err = m.sup.Add(driver.ServiceConfig(inst))
		if err != nil {
			return nil, err
		}
	}
	if err := svc.Start(ctx); err != nil {
		return nil, err
	}
	return inst, nil
}

// Stop, örneğin sunucusunu durdurur.
func (m *Manager) Stop(name string) error {
	if m.sup == nil {
		return nil
	}
	svc, ok := m.sup.Get("db-" + name)
	if !ok {
		return nil
	}
	svc.Stop()
	return nil
}

// Get, örneğin üstverisini okur.
func (m *Manager) Get(name string) (*Instance, error) {
	data, err := os.ReadFile(m.metaPath(name))
	if err != nil {
		return nil, fmt.Errorf("database: %q örneği bulunamadı", name)
	}
	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return nil, fmt.Errorf("database: %q üstverisi bozuk: %w", name, err)
	}
	return &inst, nil
}

// List, tüm örnekleri ada göre sıralı döner.
func (m *Manager) List() ([]*Instance, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []*Instance
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if inst, err := m.Get(name); err == nil {
			out = append(out, inst)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove, örneği ve verisini siler.
//
// Anlık görüntüler kasten silinmiyor: örneği silmek "verilerden tümüyle
// vazgeçtim" demek olmayabilir ve yedeği yanlışlıkla silmek geri alınamaz.
func (m *Manager) Remove(name string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	m.Stop(name)

	// Önce üstveri: silme yarıda kalırsa örnek "kurulu" görünmesin.
	if err := os.Remove(m.metaPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(inst.DataDir); err != nil {
		return err
	}
	m.alloc.Release(inst.Port)
	return nil
}

// Snapshot, örneğin veri dizininin birebir kopyasını alır.
//
// # Neden SQL dökümü değil
//
// İlk tasarım pg_dumpall/mysqldump kullanıyordu. Gerçek bir kümede
// denenince kırıldı: pg_dumpall'ın ürettiği dosya "DROP ROLE postgres"
// içeriyor ve bunu çalışan bir kümeye uygulamak "current user cannot be
// dropped" ile düşüyor — üstelik veritabanı zaten düşürüldükten sonra.
// Yani yarım geri yükleme: kullanıcı başarılı sandığı bir durumla değil,
// eskisinden kötü bir durumla kalıyor.
//
// Veri dizinini kopyalamak bu sorunun tamamını ortadan kaldırıyor ve
// aslında istenen şeye daha yakın: "geçişten önceki hâle dön" demek, bit
// bit o hâle dönmek demek. Roller, ayarlar, diziler, her şey birlikte
// geliyor.
//
// Bedeli: kopyalama sırasında sunucunun durması gerekiyor. Çalışan bir
// veritabanının veri dizinini kopyalamak tutarsız bir kopya üretir. Örnek
// çalışıyorsa durduruluyor ve işlem bitince yeniden başlatılıyor.
//
// Sürümler arası taşıma için SQL dökümü hâlâ gerekli; o Export ile.
func (m *Manager) Snapshot(ctx context.Context, name, tag string) (string, error) {
	inst, err := m.Get(name)
	if err != nil {
		return "", err
	}
	if !validTag(tag) {
		return "", fmt.Errorf("database: geçersiz etiket %q", tag)
	}

	wasRunning := m.running(inst)
	if wasRunning {
		m.Stop(name)
	}

	dir := m.snapshotDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, tag)

	// Geçici dizine kopyalayıp yerine taşıyoruz: yarıda kalmış bir kopya
	// geri yüklenebilir görünmemeli.
	tmp := dest + ".tmp"
	os.RemoveAll(tmp)
	if err := copyTree(inst.DataDir, tmp); err != nil {
		os.RemoveAll(tmp)
		m.restartIfWasRunning(ctx, name, wasRunning)
		return "", fmt.Errorf("database: anlık görüntü alınamadı: %w", err)
	}
	os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		os.RemoveAll(tmp)
		m.restartIfWasRunning(ctx, name, wasRunning)
		return "", err
	}

	if err := m.restartIfWasRunning(ctx, name, wasRunning); err != nil {
		return dest, err
	}
	return dest, nil
}

// Restore, bir anlık görüntüyü geri yükler.
//
// Eski veri dizini silinmiyor, yana taşınıyor: kopyalama yarıda kalırsa
// kullanıcı hem eski hem yeni veriden olmasın. Kopyalama başarılı olduktan
// sonra siliniyor.
func (m *Manager) Restore(ctx context.Context, name, tag string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	src := filepath.Join(m.snapshotDir(name), tag)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return fmt.Errorf("database: %q anlık görüntüsü bulunamadı", tag)
	}

	wasRunning := m.running(inst)
	if wasRunning {
		m.Stop(name)
	}

	backup := inst.DataDir + ".geri-alinacak"
	os.RemoveAll(backup)
	if err := os.Rename(inst.DataDir, backup); err != nil {
		m.restartIfWasRunning(ctx, name, wasRunning)
		return fmt.Errorf("database: eski veri dizini kenara alınamadı: %w", err)
	}

	if err := copyTree(src, inst.DataDir); err != nil {
		// Eski hâline döndür: yarım geri yükleme, geri yüklenmemiş
		// olmaktan kötü.
		os.RemoveAll(inst.DataDir)
		os.Rename(backup, inst.DataDir)
		m.restartIfWasRunning(ctx, name, wasRunning)
		return fmt.Errorf("database: geri yükleme başarısız, eski veri korundu: %w", err)
	}
	os.RemoveAll(backup)

	return m.restartIfWasRunning(ctx, name, wasRunning)
}

// Export, örneğin tüm veritabanlarını SQL dosyasına yazar.
//
// Anlık görüntüden farkı: sürümler ve motorlar arası taşınabilir, ama
// birebir değil. Sürüm yükseltmede ve veriyi başkasına vermekte kullanılır.
// Örneğin çalışıyor olması gerekiyor; yedek aracı sunucuya bağlanıyor.
func (m *Manager) Export(ctx context.Context, name, dest string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	driver, err := DriverFor(inst.Engine)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	tmp := dest + ".tmp"
	if err := runCommand(ctx, driver.DumpCommand(inst, tmp), 30*time.Minute); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("database: dışa aktarma başarısız: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Import, SQL dosyasını örneğe uygular.
func (m *Manager) Import(ctx context.Context, name, src string) error {
	inst, err := m.Get(name)
	if err != nil {
		return err
	}
	driver, err := DriverFor(inst.Engine)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("database: dosya bulunamadı: %s", src)
	}

	cmd := driver.RestoreCommand(inst, src)
	if driver.RestoreReadsStdin() {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdin = f
	}
	if err := runCommand(ctx, cmd, 30*time.Minute); err != nil {
		return fmt.Errorf("database: içe aktarma başarısız: %w", err)
	}
	return nil
}

// reserveKnownPorts, diskteki örneklerin portlarını tahsis ediciye bildirir.
func (m *Manager) reserveKnownPorts() {
	instances, err := m.List()
	if err != nil {
		return
	}
	for _, inst := range instances {
		m.alloc.Reserve(inst.Port)
	}
}

// Running, örneğin sunucusunun çalışıp çalışmadığını söyler.
func (m *Manager) Running(name string) bool {
	inst, err := m.Get(name)
	if err != nil {
		return false
	}
	return m.running(inst)
}

// running, örneğin sunucusunun çalışıp çalışmadığını söyler.
func (m *Manager) running(inst *Instance) bool {
	if m.sup == nil {
		return false
	}
	svc, ok := m.sup.Get(inst.ServiceName())
	if !ok {
		return false
	}
	return svc.State() == supervisor.StateRunning
}

func (m *Manager) restartIfWasRunning(ctx context.Context, name string, wasRunning bool) error {
	if !wasRunning {
		return nil
	}
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := m.Start(startCtx, name); err != nil {
		return fmt.Errorf("database: işlem sonrası yeniden başlatılamadı: %w", err)
	}
	return nil
}

// copyTree, bir dizini özyinelemeli olarak kopyalar.
//
// İzinler korunuyor: PostgreSQL veri dizininin 0700 olmasını şart koşuyor
// ve daha geniş bir izinle açılmayı reddediyor.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			// Sembolik bağlar atlanıyor: veri dizininde beklenmiyor ve
			// hedefleri dizin dışına çıkabilir.
			return nil
		case !info.Mode().IsRegular():
			// Soket dosyaları (mysqld.sock) kopyalanamaz ve gerekmez.
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Snapshots, bir örneğin anlık görüntülerini sıralı döner.
func (m *Manager) Snapshots(name string) ([]string, error) {
	entries, err := os.ReadDir(m.snapshotDir(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		// Anlık görüntüler dizin; yarıda kalmış .tmp kopyaları listelenmez.
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *Manager) writeMeta(inst *Instance) error {
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	path := m.metaPath(inst.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runCommand, komutu çalıştırır ve çıktısını hataya ekler.
func runCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// exec.CommandContext ile kurulmadığı için iptali elle bağlıyoruz.
	done := make(chan error, 1)
	var out []byte
	go func() {
		var err error
		out, err = cmd.CombinedOutput()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			// Aracın kendi mesajı olmadan "exit status 1" hiçbir şey
			// anlatmıyor.
			return fmt.Errorf("%s: %w\n%s", filepath.Base(cmd.Path), err, strings.TrimSpace(string(out)))
		}
		return nil
	case <-runCtx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return fmt.Errorf("%s zaman aşımına uğradı (%s)", filepath.Base(cmd.Path), timeout)
	}
}

func validInstanceName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// validTag, anlık görüntü etiketini denetler. Etiket dosya adına giriyor.
func validTag(tag string) bool {
	if tag == "" || len(tag) > 128 || strings.Contains(tag, "..") {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}
