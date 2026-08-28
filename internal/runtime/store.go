package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store, kurulu runtime'ları diskte yönetir.
//
// Dizin düzeni:
//
//	root/php/8.3.14/       kurulum (arşivin açılmış hâli, dokunulmaz)
//	root/php/8.3.14.json   üstveri (hangi paketten, ne zaman)
//	root/.cache/<sha>.zip  indirilen arşivler
//	root/.tmp/...          açma sırasında kullanılan geçici dizinler
//
// Üstveri kurulum dizininin *yanında* durur, içinde değil: kurulum dizini
// arşivden çıktığı gibi kalsın, PATH ve kabuklar oraya baksın.
type Store struct {
	root   string
	client *http.Client
}

// NewStore, verilen kök dizinde bir depo açar.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// WithClient, indirmelerde kullanılacak HTTP istemcisini değiştirir
// (testler için).
func (s *Store) WithClient(c *http.Client) *Store {
	s.client = c
	return s
}

// Root, deponun kök dizini.
func (s *Store) Root() string { return s.root }

func (s *Store) cacheDir() string { return filepath.Join(s.root, ".cache") }
func (s *Store) tmpDir() string   { return filepath.Join(s.root, ".tmp") }

func (s *Store) versionDir(name, version string) string {
	return filepath.Join(s.root, name, version)
}

func (s *Store) metaPath(name, version string) string {
	return filepath.Join(s.root, name, version+".json")
}

// Installed, kurulmuş bir runtime.
type Installed struct {
	Package     Package   `json:"package"`
	Dir         string    `json:"-"`
	InstalledAt time.Time `json:"installedAt"`
}

// Bin, mantıksal ada karşılık gelen çalıştırılabilirin tam yolunu döner.
func (i Installed) Bin(logical string) (string, error) {
	rel, ok := i.Package.Bin[logical]
	if !ok {
		return "", fmt.Errorf("runtime: %s içinde %q diye bir ikili tanımlı değil", i.Package.ID(), logical)
	}
	return filepath.Join(i.Dir, filepath.FromSlash(rel)), nil
}

// Install, paketi indirir, doğrular ve kurar.
//
// Zaten kuruluysa hiçbir şey yapmaz; komut yeniden çalıştırılabilir olsun.
func (s *Store) Install(ctx context.Context, p Package, onProgress func(Progress)) (Installed, error) {
	if err := p.Validate(); err != nil {
		return Installed{}, err
	}
	if existing, ok, err := s.lookup(p.Name, p.Version); err != nil {
		return Installed{}, err
	} else if ok {
		return existing, nil
	}

	for _, dir := range []string{s.cacheDir(), s.tmpDir(), filepath.Join(s.root, p.Name)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Installed{}, err
		}
	}

	// Arşiv, SHA256'sıyla adlandırılıyor: aynı dosya iki paket tarafından
	// paylaşılıyorsa bir kez iniyor ve önbellek kendiliğinden doğrulanıyor.
	ext := ".zip"
	if p.Archive == "tar.gz" {
		ext = ".tar.gz"
	}
	archivePath := filepath.Join(s.cacheDir(), strings.ToLower(p.SHA256)+ext)

	if !fileExists(archivePath) {
		if err := download(ctx, s.client, p.URL, archivePath, p.SHA256, p.Size, onProgress); err != nil {
			return Installed{}, err
		}
	}

	stage, err := os.MkdirTemp(s.tmpDir(), p.Name+"-"+p.Version+"-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(stage)

	if err := extract(archivePath, stage, p.Archive, p.StripPrefix); err != nil {
		return Installed{}, err
	}

	// Manifest'in vaat ettiği ikililer gerçekten var mı? İlk kullanımda
	// "dosya bulunamadı" almaktansa kurulumda öğrenmek çok daha iyi.
	for logical, rel := range p.Bin {
		if !fileExists(filepath.Join(stage, filepath.FromSlash(rel))) {
			return Installed{}, fmt.Errorf(
				"runtime: %s arşivinde %q (%s) bulunamadı; manifest kaydı yanlış olabilir",
				p.ID(), logical, rel)
		}
	}

	target := s.versionDir(p.Name, p.Version)
	if err := os.Rename(stage, target); err != nil {
		// Araya başka bir kurulum girmiş olabilir.
		if fileExists(target) {
			if existing, ok, lerr := s.lookup(p.Name, p.Version); lerr == nil && ok {
				return existing, nil
			}
		}
		return Installed{}, fmt.Errorf("runtime: kurulum yerine taşınamadı: %w", err)
	}

	inst := Installed{Package: p, Dir: target, InstalledAt: time.Now().UTC()}
	if err := s.writeMeta(inst); err != nil {
		os.RemoveAll(target)
		return Installed{}, err
	}
	return inst, nil
}

func (s *Store) writeMeta(inst Installed) error {
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	path := s.metaPath(inst.Package.Name, inst.Package.Version)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lookup, tek bir kurulumu okur.
func (s *Store) lookup(name, version string) (Installed, bool, error) {
	dir := s.versionDir(name, version)
	if !dirExists(dir) {
		return Installed{}, false, nil
	}

	data, err := os.ReadFile(s.metaPath(name, version))
	if err != nil {
		// Dizin var ama üstveri yok: yarım kalmış ya da elle kopyalanmış
		// bir kurulum. Kurulu saymıyoruz ki üzerine düzgünü gelsin.
		return Installed{}, false, nil
	}
	var inst Installed
	if err := json.Unmarshal(data, &inst); err != nil {
		return Installed{}, false, nil
	}
	inst.Dir = dir
	return inst, true, nil
}

// List, kurulu runtime'ları ada ve sürüme göre sıralı döner.
func (s *Store) List() ([]Installed, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []Installed
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			if inst, ok, _ := s.lookup(e.Name(), v.Name()); ok {
				out = append(out, inst)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Package.Name != out[j].Package.Name {
			return out[i].Package.Name < out[j].Package.Name
		}
		return ParseVersion(out[i].Package.Version).Compare(ParseVersion(out[j].Package.Version)) > 0
	})
	return out, nil
}

// Resolve, belirtece ("php@8.3") uyan kurulu sürümlerin en yenisini döner.
func (s *Store) Resolve(spec string) (Installed, bool, error) {
	name, constraint := SplitSpec(spec)
	all, err := s.List()
	if err != nil {
		return Installed{}, false, err
	}
	for _, inst := range all { // List zaten sürüme göre azalan sırada
		if inst.Package.Name == name && ParseVersion(inst.Package.Version).Matches(constraint) {
			return inst, true, nil
		}
	}
	return Installed{}, false, nil
}

// Remove, kurulu bir sürümü siler.
func (s *Store) Remove(name, version string) error {
	dir := s.versionDir(name, version)
	if !dirExists(dir) {
		return fmt.Errorf("runtime: %s@%s kurulu değil", name, version)
	}
	// Önce üstveriyi sil: silme yarıda kalırsa kurulum "kurulu" görünmesin.
	os.Remove(s.metaPath(name, version))
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// Ada ait başka sürüm kalmadıysa boş dizini de topla.
	os.Remove(filepath.Join(s.root, name))
	return nil
}

// PruneCache, indirilmiş arşivleri siler. Kurulumlara dokunmaz.
func (s *Store) PruneCache() (freed int64, err error) {
	entries, err := os.ReadDir(s.cacheDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(s.cacheDir(), e.Name())); err == nil {
			freed += info.Size()
		}
	}
	return freed, nil
}

// PruneStale, yarım kalmış geçici dizinleri temizler.
//
// Kurulum sırasında süreç öldürülürse .tmp altında artıklar kalır; bunlar
// hiçbir zaman "kurulu" sayılmaz ama yer kaplar.
func (s *Store) PruneStale(olderThan time.Duration) error {
	entries, err := os.ReadDir(s.tmpDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.RemoveAll(filepath.Join(s.tmpDir(), e.Name()))
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
