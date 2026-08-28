// Package phpini, proje başına php.ini üretir.
//
// PHP yönergeleri dosya sırasına göre uygular: aynı yönerge iki kez geçerse
// sonuncusu kazanır. Bu yüzden katmanlama basit — temel dosyanın metnini
// olduğu gibi alıp altına kendi bloğumuzu ekliyoruz. Temel dosyayı
// ayrıştırmaya çalışmak (php.ini biçimi bölümler, koşullu bloklar ve satır
// devamları içerir) hem gereksiz hem kırılgan olurdu.
//
// Proje başına ayrı bir php.ini, DevBox'ın Laragon'dan ayrıldığı yerlerden
// biri: bir projede 512 MB bellek ve Xdebug açıkken, diğerinde varsayılan
// ayarlarla çalışabilmek gerekiyor.
package phpini

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Xdebug, hata ayıklayıcı ayarları.
type Xdebug struct {
	// Extension, php_xdebug.dll dosyasının yolu. Boşsa yalnız ayarlar
	// yazılır (uzantı zaten temel dosyada yükleniyorsa).
	Extension string

	// Mode, xdebug.mode değeri. Boşsa "debug".
	Mode string

	// ClientHost ve ClientPort, IDE'nin dinlediği adres.
	ClientHost string
	ClientPort int

	// StartWithRequest: "yes", "no" ya da "trigger". Boşsa "trigger" —
	// her istekte IDE'ye bağlanmayı denemek, IDE kapalıyken her isteği
	// yavaşlatır.
	StartWithRequest string
}

// Config, üretilecek php.ini'nin girdileri.
type Config struct {
	// BaseFile, temel alınacak php.ini (genelde runtime'ın
	// php.ini-development'ı). Boşsa yalnız DevBox bloğu yazılır.
	BaseFile string

	// ExtensionDir, uzantıların bulunduğu dizin.
	ExtensionDir string

	// Extensions, yüklenecek uzantı adları ("gd", "intl", "pdo_mysql").
	Extensions []string

	// Settings, projeye özel yönergeler. DevBox varsayılanlarını geçersiz
	// kılar.
	Settings map[string]string

	// Xdebug, nil değilse hata ayıklayıcı açılır.
	Xdebug *Xdebug
}

// Defaults, DevBox'ın geliştirme için makul bulduğu ayarlar.
//
// cgi.fix_pathinfo=0 bir tercih değil, güvenlik gereği: 1 olduğunda php-cgi
// "/yuklemeler/kedi.jpg/x.php" gibi bir yolu geriye doğru yorumlayıp
// yüklenmiş bir resmi PHP olarak çalıştırabiliyor. Web sunucusu
// yapılandırmalarımız bunu ayrıca engelliyor; burada kapatmak üçüncü
// savunma hattı.
func Defaults() map[string]string {
	return map[string]string{
		"cgi.fix_pathinfo":       "0",
		"display_errors":         "On",
		"display_startup_errors": "On",
		"error_reporting":        "E_ALL",
		"log_errors":             "On",
		"memory_limit":           "512M",
		"max_execution_time":     "120",
		"upload_max_filesize":    "128M",
		"post_max_size":          "128M",
		"date.timezone":          "UTC",
		"opcache.enable":         "1",
		"opcache.enable_cli":     "0",
		// Geliştirmede dosya değişikliği anında görünmeli; üretimdeki
		// varsayılan (2 saniye) düzenle-yenile döngüsünü bozuyor.
		"opcache.revalidate_freq":     "0",
		"opcache.validate_timestamps": "1",
	}
}

const header = "; DevBox tarafından üretildi — elle düzenlemeyin.\n" +
	"; Bu blok temel dosyadan sonra geldiği için onun ayarlarını geçersiz kılar.\n"

// Render, php.ini içeriğini üretir.
func Render(cfg Config) (string, error) {
	settings := Defaults()
	for k, v := range cfg.Settings {
		settings[k] = v
	}
	for k, v := range settings {
		if err := validateDirective(k, v); err != nil {
			return "", err
		}
	}

	var sb strings.Builder

	if cfg.BaseFile != "" {
		base, err := os.ReadFile(cfg.BaseFile)
		if err != nil {
			return "", fmt.Errorf("phpini: temel dosya okunamadı: %w", err)
		}
		sb.Write(base)
		if !strings.HasSuffix(string(base), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(header)
	sb.WriteString("[PHP]\n")

	if cfg.ExtensionDir != "" {
		if strings.ContainsAny(cfg.ExtensionDir, "\r\n\"") {
			return "", fmt.Errorf("phpini: geçersiz uzantı dizini %q", cfg.ExtensionDir)
		}
		fmt.Fprintf(&sb, "extension_dir = \"%s\"\n", toIniPath(cfg.ExtensionDir))
	}

	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s = %s\n", k, settings[k])
	}

	if len(cfg.Extensions) > 0 {
		sb.WriteString("\n; Uzantılar\n")
		for _, ext := range cfg.Extensions {
			if !validExtensionName(ext) {
				return "", fmt.Errorf("phpini: geçersiz uzantı adı %q", ext)
			}
			fmt.Fprintf(&sb, "extension = %s\n", ext)
		}
	}

	if cfg.Xdebug != nil {
		block, err := renderXdebug(cfg.Xdebug)
		if err != nil {
			return "", err
		}
		sb.WriteString(block)
	}

	return sb.String(), nil
}

func renderXdebug(x *Xdebug) (string, error) {
	mode := x.Mode
	if mode == "" {
		mode = "debug"
	}
	start := x.StartWithRequest
	if start == "" {
		// Her istekte IDE'ye bağlanmayı denemek, IDE kapalıyken her isteği
		// bağlantı zaman aşımı kadar yavaşlatıyor. "trigger" yalnız
		// istendiğinde bağlanır.
		start = "trigger"
	}
	host := x.ClientHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := x.ClientPort
	if port == 0 {
		port = 9003 // Xdebug 3 varsayılanı
	}

	for _, v := range []string{mode, start, host} {
		if strings.ContainsAny(v, "\r\n\"") {
			return "", fmt.Errorf("phpini: geçersiz xdebug değeri %q", v)
		}
	}

	var sb strings.Builder
	sb.WriteString("\n; Xdebug\n")
	if x.Extension != "" {
		if strings.ContainsAny(x.Extension, "\r\n\"") {
			return "", fmt.Errorf("phpini: geçersiz xdebug uzantı yolu %q", x.Extension)
		}
		// zend_extension, normal uzantılardan farklı bir yönerge; Xdebug
		// extension= ile yüklenmez.
		fmt.Fprintf(&sb, "zend_extension = \"%s\"\n", toIniPath(x.Extension))
	}
	fmt.Fprintf(&sb, "xdebug.mode = %s\n", mode)
	fmt.Fprintf(&sb, "xdebug.start_with_request = %s\n", start)
	fmt.Fprintf(&sb, "xdebug.client_host = %s\n", host)
	fmt.Fprintf(&sb, "xdebug.client_port = %s\n", strconv.Itoa(port))
	// Xdebug açıkken opcache ile birlikte çalışmak sorun çıkarabiliyor.
	sb.WriteString("opcache.enable = 0\n")
	return sb.String(), nil
}

// Write, üretilen php.ini'yi dizine yazar ve dosyanın yolunu döner.
//
// php-cgi'ye "-c <dizin>" verilir; PHP o dizindeki php.ini'yi okur.
func Write(dir string, cfg Config) (string, error) {
	content, err := Render(cfg)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, "php.ini")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// validateDirective, yönergenin dosyaya güvenle yazılabileceğini denetler.
//
// Değerler doğrudan dosyaya gittiği için satır sonu içeren bir değer,
// istemediğimiz yönergeler eklemek demek.
func validateDirective(key, value string) error {
	if key == "" {
		return fmt.Errorf("phpini: boş yönerge adı")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_':
		default:
			return fmt.Errorf("phpini: geçersiz yönerge adı %q", key)
		}
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("phpini: %s değeri satır sonu içeriyor", key)
	}
	return nil
}

func validExtensionName(ext string) bool {
	if ext == "" || len(ext) > 64 {
		return false
	}
	for i := 0; i < len(ext); i++ {
		c := ext[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// toIniPath, yolu php.ini'ye yazılabilir hâle getirir.
//
// PHP Windows'ta her iki bölüyü de kabul ediyor ama ters bölü tırnak içinde
// kaçış olarak yorumlanabildiği için eğik bölü daha güvenli.
func toIniPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}
