// Package webserver, Apache ve Nginx için site yapılandırması üretir.
//
// Kenar proxy 80/443'ü dinlediği için bu sunucular loopback'te düz HTTP
// konuşuyor; TLS, alan adı yönlendirmesi ve sertifika onların derdi değil.
// Buradaki tek iş, projenin beklediği sunucuya doğru vhost'u yazmak.
//
// Neden hâlâ Apache ve Nginx var: DevBox PHP'yi kendi de sunabiliyor (bkz.
// internal/web), ama gerçek projeler .htaccess'e, nginx yeniden yazma
// kurallarına ve özel location bloklarına bağımlı. Onları taklit etmek yerine
// gerçeğini çalıştırmak daha doğru.
package webserver

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Site, tek bir sitenin yapılandırması.
type Site struct {
	// Name, dosya adlarında ve upstream adlarında kullanılır.
	Name string

	// Domain ve Aliases, sunucunun eşleşeceği adlar.
	Domain  string
	Aliases []string

	// DocumentRoot, sitenin kök dizini (Laravel'de public/).
	DocumentRoot string

	// Listen, sunucunun dinleyeceği loopback adresi.
	Listen string

	// PHPBackends, php-cgi işçilerinin FastCGI adresleri. Boşsa site
	// yalnız statik dosya sunar.
	PHPBackends []string

	// Index, dizin isteklerinde denenecek dosyalar.
	Index []string

	// FrontController, diskte karşılığı olmayan yolların yönlendirileceği
	// betik. Boşsa "index.php".
	FrontController string

	// LogDir, erişim ve hata günlüklerinin yazılacağı dizin.
	LogDir string
}

func (s *Site) applyDefaults() {
	if len(s.Index) == 0 {
		s.Index = []string{"index.php", "index.html"}
	}
	if s.FrontController == "" {
		s.FrontController = "index.php"
	}
}

// Validate, sitenin yapılandırılabilir olduğunu denetler.
//
// Değerler doğrudan yapılandırma dosyasına yazıldığı için bu denetimler
// kozmetik değil: satır sonu ya da tırnak içeren bir alan adı, üretilen
// dosyaya istemediğimiz yönergeler eklemek demek.
func (s *Site) Validate() error {
	switch {
	case s.Name == "":
		return fmt.Errorf("webserver: site adı boş")
	case !validIdentifier(s.Name):
		return fmt.Errorf("webserver: geçersiz site adı %q", s.Name)
	case s.Domain == "":
		return fmt.Errorf("webserver: %s için alan adı boş", s.Name)
	case s.DocumentRoot == "":
		return fmt.Errorf("webserver: %s için belge kökü boş", s.Name)
	case s.Listen == "":
		return fmt.Errorf("webserver: %s için dinleme adresi boş", s.Name)
	}

	for _, name := range append([]string{s.Domain}, s.Aliases...) {
		if !validDomain(name) {
			return fmt.Errorf("webserver: geçersiz alan adı %q", name)
		}
	}
	for _, addr := range append([]string{s.Listen}, s.PHPBackends...) {
		if !validAddr(addr) {
			return fmt.Errorf("webserver: geçersiz adres %q", addr)
		}
	}
	for _, index := range s.Index {
		if !validFileName(index) {
			return fmt.Errorf("webserver: geçersiz dizin dosyası %q", index)
		}
	}
	if !validFileName(s.FrontController) && s.FrontController != "" {
		return fmt.Errorf("webserver: geçersiz ön denetleyici %q", s.FrontController)
	}
	return nil
}

// ServerNames, alan adlarını boşlukla ayrılmış tek dize olarak döner.
func (s *Site) ServerNames() string {
	return strings.Join(append([]string{s.Domain}, s.Aliases...), " ")
}

// Root, belge kökünü yapılandırma dosyasına yazılacak biçime çevirir.
//
// Apache ve Nginx, Windows'ta bile eğik bölü bekler; ters bölü kaçış
// karakteri sayılır ve "C:\proje\public" sessizce bozuk bir yola dönüşür.
func (s *Site) Root() string {
	return toConfigPath(s.DocumentRoot)
}

// LogPath, verilen sonekle günlük dosyasının yolunu döner.
func (s *Site) LogPath(suffix string) string {
	if s.LogDir == "" {
		return ""
	}
	return toConfigPath(filepath.Join(s.LogDir, s.Name+"-"+suffix+".log"))
}

// IndexList, dizin dosyalarını boşlukla ayrılmış tek dize olarak döner.
func (s *Site) IndexList() string { return strings.Join(s.Index, " ") }

// UpstreamName, FastCGI havuzunun yapılandırmadaki adı.
func (s *Site) UpstreamName() string { return "devbox-" + s.Name }

// toConfigPath, dosya yolunu yapılandırma söz dizimine uygun hâle getirir.
func toConfigPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func validIdentifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

func validDomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	// Joker alt alan adı: *.magaza.test
	body := strings.TrimPrefix(s, "*.")
	if body == "" || strings.Contains(body, "..") {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

func validAddr(s string) bool {
	host, port, found := strings.Cut(s, ":")
	if !found || host == "" || port == "" {
		return false
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}

func validFileName(s string) bool {
	if s == "" || len(s) > 128 || strings.ContainsAny(s, `/\ "'`) || strings.Contains(s, "..") {
		return false
	}
	return true
}
