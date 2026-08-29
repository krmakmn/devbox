// Package projects, makinedeki DevBox projelerinin kaydını tutar ve
// onları çekirdek süreç üzerinden çalıştırır.
//
// # Neden bir kayıt gerekiyordu
//
// Faz 3'e kadar bir proje, içinde devbox.yaml olan bir dizindi: kullanıcı
// oraya gidip "devbox up" diyordu. Komut satırında bu yeterli — dizin
// zaten elinizin altında. Grafik arayüzde değil: "projelerim" diye bir
// liste gösterecekseniz, makinede hangi projelerin olduğunu bir yerde
// tutmanız gerekiyor. Laragon bunu "www klasörünün altındaki her şey"
// diye çözüyor; o kural projeleri tek bir dizine hapsediyor ve depo
// düzeninizi araca uydurmanızı istiyor. Burada tersi: proje nerede olursa
// olsun kaydediliyor, kayıt yalnız bir işaretçi.
//
// # Kayıt neden gerçeğin kaynağı değil
//
// Kaydın tuttuğu tek kalıcı bilgi dizin yolu. Ad, alan adı, sunucu — hepsi
// her okumada devbox.yaml'dan tazeleniyor. Aksi hâlde kullanıcı
// devbox.yaml'da alan adını değiştirdiğinde arayüz eski değeri gösterir ve
// hangisinin doğru olduğu belirsizleşir. Depo dosyası gerçeğin kaynağı;
// kayıt yalnız "şu dizinlere bak" diyen bir liste.
package projects

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/project"
)

// Entry, kayıttaki tek bir satır. Diske yazılan tek şey bu.
type Entry struct {
	// Dir, projenin mutlak dizini.
	Dir string `json:"dir"`

	// AddedAt, kayda eklendiği an.
	AddedAt time.Time `json:"addedAt"`
}

// Project, kayıttaki bir projenin okunmuş hâli.
type Project struct {
	Name    string    `json:"name"`
	Dir     string    `json:"dir"`
	Domain  string    `json:"domain"`
	Server  string    `json:"server"`
	AddedAt time.Time `json:"addedAt"`

	// Missing, dizin ya da devbox.yaml artık yoksa true.
	//
	// Böyle bir girdi kendiliğinden silinmiyor: kullanıcı diski takmayı
	// unutmuş ya da dalı değiştirmiş olabilir. Sessizce silmek, geri
	// alınamayan bir karar; göstermek ise sorunu anlatıyor.
	Missing bool `json:"missing,omitempty"`

	// Error, devbox.yaml okunamadıysa sebebi.
	Error string `json:"error,omitempty"`
}

// Registry, proje listesini diskte tutar.
type Registry struct {
	path string
	mu   sync.Mutex
}

// Open, verilen dosyadaki kaydı açar. Dosya yoksa boş kayıt döner.
func Open(path string) (*Registry, error) {
	if path == "" {
		return nil, fmt.Errorf("projects: kayıt dosyası yolu boş")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("projects: kayıt dizini oluşturulamadı: %w", err)
	}
	return &Registry{path: path}, nil
}

// Add, bir dizini kayda ekler ve okunmuş hâlini döner.
func (r *Registry) Add(dir string) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}
	cfg, err := project.Load(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return Project{}, fmt.Errorf("%s içinde %s yok; önce \"devbox init\" çalıştırın",
				abs, project.FileName)
		}
		return Project{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.read()
	if err != nil {
		return Project{}, err
	}
	for _, e := range entries {
		if sameDir(e.Dir, abs) {
			return r.describe(e), nil // zaten kayıtlı; ekleme sessizce başarılı
		}
	}
	// Ad çakışması: iki farklı dizin aynı adı taşıyorsa arayüzde ve
	// servis adlarında birbirine karışırlar.
	for _, e := range entries {
		if p := r.describe(e); p.Name == cfg.Name {
			return Project{}, fmt.Errorf("%q adı zaten kayıtlı (%s); devbox.yaml'daki name alanını değiştirin",
				cfg.Name, e.Dir)
		}
	}

	entries = append(entries, Entry{Dir: abs, AddedAt: time.Now()})
	if err := r.write(entries); err != nil {
		return Project{}, err
	}
	return Project{
		Name: cfg.Name, Dir: abs, Domain: cfg.Domain,
		Server: cfg.Server, AddedAt: time.Now(),
	}, nil
}

// Remove, projeyi kayıttan çıkarır. Dizine dokunmaz.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.read()
	if err != nil {
		return err
	}
	kept := make([]Entry, 0, len(entries))
	var found bool
	for _, e := range entries {
		if r.describe(e).Name == name || sameDir(e.Dir, name) {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return fmt.Errorf("projects: kayıtlı proje bulunamadı: %q", name)
	}
	return r.write(kept)
}

// List, kayıtlı projeleri ada göre sıralı döner.
func (r *Registry) List() ([]Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.read()
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(entries))
	for _, e := range entries {
		out = append(out, r.describe(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get, ada göre projeyi döner.
func (r *Registry) Get(name string) (Project, bool) {
	list, err := r.List()
	if err != nil {
		return Project{}, false
	}
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// describe, bir girdiyi devbox.yaml'ı okuyarak tamamlar.
func (r *Registry) describe(e Entry) Project {
	p := Project{Dir: e.Dir, AddedAt: e.AddedAt, Name: filepath.Base(e.Dir)}

	cfg, err := project.Load(e.Dir)
	if err != nil {
		p.Missing = os.IsNotExist(err)
		p.Error = err.Error()
		return p
	}
	p.Name = cfg.Name
	p.Domain = cfg.Domain
	p.Server = cfg.Server
	return p
}

// read, kayıt dosyasını okur. Çağıranın kilidi tutuyor olması gerekiyor.
func (r *Registry) read() ([]Entry, error) {
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("projects: kayıt okunamadı: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("projects: kayıt bozuk (%s): %w", r.path, err)
	}
	return entries, nil
}

// write, kaydı atomik olarak yazar.
//
// Geçici dosya + rename: yazma yarıda kalırsa kullanıcı yarım bir kayıt
// dosyasıyla değil, eskisiyle kalır.
func (r *Registry) write(entries []Entry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("projects: kayıt yazılamadı: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("projects: kayıt yerine konamadı: %w", err)
	}
	return nil
}

// sameDir, iki yolun aynı dizini gösterip göstermediğini söyler.
//
// Windows'ta büyük/küçük harf ayrımı yok; "C:\Kod\Magaza" ile
// "c:\kod\magaza" aynı dizin. Karşılaştırmayı buna göre yapmazsak aynı
// proje iki kez kaydedilir.
func sameDir(a, b string) bool {
	return normalizeDir(a) == normalizeDir(b)
}
