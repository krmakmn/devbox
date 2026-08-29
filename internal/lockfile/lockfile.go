// Package lockfile, bir projenin kullandığı sürümleri kaydeder.
//
// # Neden gerekiyor
//
// devbox.yaml "php: 8.3" diyor. Bu, 8.3.0 ile 8.3.14 arasında herhangi
// bir sürüm demek — ve aradaki fark bazen bir hatanın sizde görünüp
// ekip arkadaşınızda görünmemesi anlamına geliyor. Kilit dosyası
// makinede gerçekten ne çalıştığını yazıyor: PHP'nin tam sürümü,
// veritabanı motorunun sürümü, konteyner imajının etiketiyle birlikte
// sabitlenmiş hâli.
//
// # devbox.yaml ile farkı
//
// devbox.yaml niyeti anlatıyor ("PHP 8.3 istiyorum"), kilit dosyası
// gerçekleşeni ("8.3.14 çalıştı"). İkisi de depoya giriyor ama ayrı
// dosyalar; çünkü niyeti insan yazıyor, gerçekleşeni araç.
//
// # Neden kendiliğinden uygulanmıyor
//
// Kilit dosyası bir rapor, bir zorlayıcı değil. Sürümü zorla eşlemek,
// olmayan bir PHP'yi indirmeyi gerektirir — o da imzalı manifest
// altyapısına bağlı ve henüz yok. Bugün yaptığı şey farkı göstermek:
// "ekipte 8.3.14 var, sende 8.2.9". Bu tek başına, sorunu saatlerce
// aramaktan iyi.
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileName, proje kökünde aranan dosya.
const FileName = "devbox.lock"

// Entry, kilitlenen tek bir bileşen.
type Entry struct {
	// Kind, bileşen türü ("php", "database", "service", "container").
	Kind string `json:"kind"`

	// Name, bileşenin adı ("php", "magaza-db", "redis", "api").
	Name string `json:"name"`

	// Requested, devbox.yaml'da istenen sürüm. Boş olabilir.
	Requested string `json:"requested,omitempty"`

	// Resolved, gerçekten çalışan sürüm.
	Resolved string `json:"resolved"`

	// Source, sürümün nereden geldiği (çalıştırılabilirin yolu, imaj adı).
	Source string `json:"source,omitempty"`
}

// Lock, kilit dosyasının içeriği.
type Lock struct {
	// Version, dosya biçiminin sürümü.
	Version int `json:"version"`

	// Project, projenin adı.
	Project string `json:"project"`

	// GeneratedAt, oluşturulma anı.
	GeneratedAt time.Time `json:"generatedAt"`

	// Platform, kaydın alındığı işletim sistemi/mimari.
	Platform string `json:"platform"`

	// Entries, kilitlenen bileşenler.
	Entries []Entry `json:"entries"`
}

// CurrentVersion, ürettiğimiz dosya biçimi.
const CurrentVersion = 1

// Path, proje dizinindeki kilit dosyasının yolu.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// New, boş bir kilit oluşturur.
func New(project, platform string) *Lock {
	return &Lock{
		Version:     CurrentVersion,
		Project:     project,
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Platform:    platform,
	}
}

// Add, bir bileşen ekler.
func (l *Lock) Add(entry Entry) {
	l.Entries = append(l.Entries, entry)
}

// sortEntries, girdileri kararlı bir sıraya sokar.
//
// Sıra önemli: kilit dosyası depoya giriyor ve her çalıştırmada farklı
// sıralanırsa her seferinde anlamsız bir fark üretir.
func (l *Lock) sortEntries() {
	sort.Slice(l.Entries, func(i, j int) bool {
		if l.Entries[i].Kind != l.Entries[j].Kind {
			return l.Entries[i].Kind < l.Entries[j].Kind
		}
		return l.Entries[i].Name < l.Entries[j].Name
	})
}

// Save, kilidi proje dizinine yazar.
func (l *Lock) Save(dir string) error {
	l.sortEntries()
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := Path(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("lockfile: yazılamadı: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("lockfile: yerine konamadı: %w", err)
	}
	return nil
}

// Load, proje dizinindeki kilidi okur.
func Load(dir string) (*Lock, error) {
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		return nil, err
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("lockfile: %s bozuk: %w", Path(dir), err)
	}
	if lock.Version > CurrentVersion {
		return nil, fmt.Errorf(
			"lockfile: dosya biçimi sürümü %d, bu DevBox en fazla %d okuyabiliyor; DevBox'ı güncelleyin",
			lock.Version, CurrentVersion)
	}
	return &lock, nil
}

// Difference, iki kilit arasındaki fark.
type Difference struct {
	Kind     string
	Name     string
	Expected string
	Actual   string
}

func (d Difference) String() string {
	switch {
	case d.Expected == "":
		return fmt.Sprintf("%s/%s: kilitte yok, makinede %s", d.Kind, d.Name, d.Actual)
	case d.Actual == "":
		return fmt.Sprintf("%s/%s: kilitte %s, makinede yok", d.Kind, d.Name, d.Expected)
	default:
		return fmt.Sprintf("%s/%s: kilitte %s, makinede %s", d.Kind, d.Name, d.Expected, d.Actual)
	}
}

// Compare, kilitteki sürümlerle şu ankileri karşılaştırır.
//
// Eksik ve fazla bileşenler de fark sayılıyor: ekip arkadaşının
// projesinde Redis varken sizde olmaması, sürüm farkı kadar önemli.
func Compare(expected, actual *Lock) []Difference {
	var diffs []Difference

	index := func(l *Lock) map[string]Entry {
		out := make(map[string]Entry, len(l.Entries))
		for _, e := range l.Entries {
			out[e.Kind+"/"+e.Name] = e
		}
		return out
	}
	want, got := index(expected), index(actual)

	keys := make([]string, 0, len(want)+len(got))
	seen := make(map[string]bool)
	for k := range want {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range got {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		w, hasW := want[key]
		g, hasG := got[key]
		kind, name, _ := strings.Cut(key, "/")
		switch {
		case hasW && hasG && w.Resolved != g.Resolved:
			diffs = append(diffs, Difference{kind, name, w.Resolved, g.Resolved})
		case hasW && !hasG:
			diffs = append(diffs, Difference{kind, name, w.Resolved, ""})
		case !hasW && hasG:
			diffs = append(diffs, Difference{kind, name, "", g.Resolved})
		}
	}
	return diffs
}
