// Package runtime, PHP/Node/veritabanı gibi çalıştırılabilir bileşenleri
// indirir, doğrular ve sürümlü olarak kurar.
//
// Laragon'da runtime'lar elle indirilip bir klasöre atılır: bütünlük
// doğrulaması, sürüm yönetimi, geri alma ve temizlik yoktur. Buradaki
// sözleşme bunun tersi:
//
//   - Her indirme SHA256 ile doğrulanır; eşleşmeyen dosya diske kalıcı
//     olarak yazılmaz.
//   - Kurulum atomiktir: arşiv geçici dizine açılır, ancak eksiksiz
//     açıldıysa yerine taşınır. Yarım açılmış bir dizin "kurulu" görünmez.
//   - Sürümler yan yana durur; geçiş yapmak dosya taşımaz.
//   - Manifest imzalıdır: hangi sürümün nereden ineceğine dair listeyi
//     araya giren biri değiştiremesin.
package runtime

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// Manifest, kurulabilir runtime'ların listesidir.
type Manifest struct {
	// SchemaVersion, biçim değişirse eski istemcilerin anlamlı hata
	// verebilmesi için.
	SchemaVersion int `json:"schemaVersion"`

	// Runtimes, kurulabilir bileşenler.
	Runtimes []Package `json:"runtimes"`
}

// Package, tek bir runtime sürümünün tek bir platform için kaydı.
type Package struct {
	Name    string `json:"name"`    // "php", "node", "mariadb"
	Version string `json:"version"` // "8.3.14"
	OS      string `json:"os"`      // "windows", "linux"
	Arch    string `json:"arch"`    // "amd64", "arm64"

	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`

	// Archive, arşiv biçimi: "zip" ya da "tar.gz".
	Archive string `json:"archive"`

	// StripPrefix, arşivin içindeki kök dizin. Çoğu dağıtım her şeyi
	// "php-8.3.14-Win32-vs16-x64/" gibi tek bir dizine koyar; kurulum
	// dizininde bu fazladan katmanı istemiyoruz.
	StripPrefix string `json:"stripPrefix,omitempty"`

	// Bin, mantıksal ad → kurulum dizinine göre göreli yol.
	// Örn. {"php": "php.exe", "php-cgi": "php-cgi.exe"}
	Bin map[string]string `json:"bin,omitempty"`

	// Notes, kullanıcıya gösterilecek not (lisans uyarısı, ek gereksinim).
	Notes string `json:"notes,omitempty"`
}

// ID, paketin insan tarafından okunabilir kimliği.
func (p Package) ID() string { return p.Name + "@" + p.Version }

// Validate, manifestten gelen kaydın kullanılabilir olduğunu denetler.
//
// Manifest imzalı olsa bile alanları doğruluyoruz: imza "bu listeyi biz
// yayımladık" der, "içindeki her alan mantıklı" demez.
func (p Package) Validate() error {
	switch {
	case p.Name == "":
		return errors.New("runtime: paket adı boş")
	case !validPackageName(p.Name):
		return fmt.Errorf("runtime: geçersiz paket adı %q", p.Name)
	case p.Version == "":
		return fmt.Errorf("runtime: %s için sürüm boş", p.Name)
	case !validVersionString(p.Version):
		return fmt.Errorf("runtime: geçersiz sürüm %q", p.Version)
	case p.OS == "" || p.Arch == "":
		return fmt.Errorf("runtime: %s için platform belirtilmemiş", p.ID())
	case !strings.HasPrefix(p.URL, "https://"):
		// Düz HTTP'ye izin vermek, SHA256 doğrulasak bile indirme
		// adresinin değiştirilmesine kapı açar.
		return fmt.Errorf("runtime: %s için indirme adresi https değil", p.ID())
	case len(p.SHA256) != 64 || !isHex(p.SHA256):
		return fmt.Errorf("runtime: %s için SHA256 geçersiz", p.ID())
	case p.Archive != "zip" && p.Archive != "tar.gz":
		return fmt.Errorf("runtime: %s için bilinmeyen arşiv biçimi %q", p.ID(), p.Archive)
	}
	for logical, rel := range p.Bin {
		if strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\..`) {
			return fmt.Errorf("runtime: %s içinde şüpheli ikili yolu %q → %q", p.ID(), logical, rel)
		}
	}
	return nil
}

// validPackageName, adı dosya sistemine güvenle yazılabilir tutar.
func validPackageName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// validVersionString, sürümün dizin adı olarak kullanılabilir olduğunu
// denetler. Bu bir kozmetik denetim değil: sürüm dizin yoluna giriyor.
func validVersionString(v string) bool {
	if v == "" || len(v) > 64 || strings.Contains(v, "..") {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ErrUnsigned, imza doğrulanamadığında döner.
var ErrUnsigned = errors.New("runtime: manifest imzası doğrulanamadı")

// ParseManifest, manifest baytlarını çözer.
//
// pub verilmişse imza zorunludur. pub boşsa imza denetlenmez; bunu yalnız
// yerel dosyadan okunan manifestler için kullanın — uzaktan alınan imzasız
// bir manifest, saldırganın istediği ikiliyi kurdurabilmesi demektir.
func ParseManifest(data, sig []byte, pub ed25519.PublicKey) (*Manifest, error) {
	if len(pub) > 0 {
		if len(pub) != ed25519.PublicKeySize {
			return nil, errors.New("runtime: açık anahtar boyutu geçersiz")
		}
		if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, data, sig) {
			return nil, ErrUnsigned
		}
	}

	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("runtime: manifest çözülemedi: %w", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("runtime: desteklenmeyen manifest sürümü %d (bu sürüm 1 bekliyor)", m.SchemaVersion)
	}

	for i, p := range m.Runtimes {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("runtime: %d. kayıt: %w", i, err)
		}
	}
	return &m, nil
}

// Sign, manifest baytlarını imzalar. Yayın araçları ve testler için.
func Sign(data []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, data)
}

// Select, verilen belirtece ("php@8.3") uyan paketlerden bu platforma ait
// olanların en yenisini döner.
func (m *Manifest) Select(spec string) (Package, error) {
	name, constraint := SplitSpec(spec)
	candidates := m.Find(name, constraint, runtime.GOOS, runtime.GOARCH)
	if len(candidates) == 0 {
		return Package{}, fmt.Errorf("runtime: %s için %s/%s uyumlu paket yok",
			spec, runtime.GOOS, runtime.GOARCH)
	}
	return candidates[0], nil
}

// Find, ölçütlere uyan paketleri sürüme göre azalan sırada döner.
func (m *Manifest) Find(name, constraint, goos, goarch string) []Package {
	var out []Package
	for _, p := range m.Runtimes {
		if p.Name != name || p.OS != goos || p.Arch != goarch {
			continue
		}
		if !ParseVersion(p.Version).Matches(constraint) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return ParseVersion(out[i].Version).Compare(ParseVersion(out[j].Version)) > 0
	})
	return out
}

// Names, manifestteki farklı runtime adlarını sıralı döner.
func (m *Manifest) Names() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range m.Runtimes {
		if !seen[p.Name] {
			seen[p.Name] = true
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}
