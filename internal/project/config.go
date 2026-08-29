// Package project, bir projenin DevBox yapılandırmasını (devbox.yaml) okur.
//
// Bu dosya DevBox'ın Laragon'dan en belirgin ayrıldığı yer: ortam makineye
// değil depoya yazılıyor. Ekip arkadaşı klonlayıp "devbox up" diyor ve aynı
// PHP sürümü, aynı uzantılar, aynı web sunucusu, aynı alan adı geliyor.
//
// # Neden YAML ve neden bir bağımlılık
//
// DevBox'ın tek dış bağımlılığı gopkg.in/yaml.v3. YAML'i elle ayrıştırmak
// ilk bakışta mümkün görünüyor (bize yalnız iç içe eşlemeler ve dizeler
// lazım) ama biçimin yüzeyi çok geniş: çok satırlı dizeler, çapalar,
// alıntılama kuralları, girinti incelikleri. Bunların her biri
// kullanıcının yazıp da çalışmayacağı bir şey, yani sonu gelmeyen
// "neden bu satır çalışmıyor" hataları. Tek, olgun ve kendi bağımlılığı
// olmayan bir kütüphane bu riskten çok daha ucuz.
package project

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/krmakmn/devbox/internal/cron"
)

// FileName, proje kökünde aranan dosya.
const FileName = "devbox.yaml"

// Sunucu seçenekleri.
const (
	// ServerDevBox, PHP'yi DevBox'ın kendi FastCGI köprüsüyle sunar.
	// En hızlısı ve ek kurulum istemez; .htaccess desteği yoktur.
	ServerDevBox = "devbox"

	// ServerApache, Apache httpd. .htaccess'e bağımlı projeler için.
	ServerApache = "apache"

	// ServerNginx, nginx.
	ServerNginx = "nginx"

	// ServerProxy, isteği başka bir adrese iletir (Vite, Next.js gibi
	// kendi geliştirme sunucusunu çalıştıran projeler için).
	ServerProxy = "proxy"
)

// Config, devbox.yaml'ın içeriği.
type Config struct {
	// Name, projenin adı. Dosya adlarında ve günlüklerde kullanılır.
	Name string `yaml:"name"`

	// Domain, sitenin alan adı (ör. magaza.test).
	Domain string `yaml:"domain"`

	// Aliases, ek alan adları.
	Aliases []string `yaml:"aliases,omitempty"`

	// Server, isteği kimin karşılayacağı.
	Server string `yaml:"server"`

	// Root, belge kökü — proje dizinine göre. Laravel'de "public".
	Root string `yaml:"root,omitempty"`

	// Proxy, Server "proxy" ise iletilecek adres.
	Proxy string `yaml:"proxy,omitempty"`

	// FrontController, diskte karşılığı olmayan yolların gideceği betik.
	FrontController string `yaml:"frontController,omitempty"`

	// PHP, PHP ayarları.
	PHP PHP `yaml:"php,omitempty"`

	// Services, ayağa kaldırılacak yan servisler ("postgres@17", "redis").
	Services []string `yaml:"services,omitempty"`

	// Env, projeye verilecek ortam değişkenleri.
	Env map[string]string `yaml:"env,omitempty"`

	// Processes, projeyle birlikte çalışacak uzun ömürlü süreçler
	// (kuyruk işçisi, Vite).
	Processes map[string]string `yaml:"processes,omitempty"`

	// Cron, zamanlanmış görevler.
	Cron []CronEntry `yaml:"cron,omitempty"`

	// Mail, yerel posta yakalayıcı ayarları.
	Mail Mail `yaml:"mail,omitempty"`

	// dir, yapılandırmanın okunduğu dizin. Dosyaya yazılmaz.
	dir string `yaml:"-"`
}

// PHP, projenin PHP ayarları.
type PHP struct {
	// Version, kullanılacak PHP sürümü ("8.3"). Boşsa en yenisi.
	Version string `yaml:"version,omitempty"`

	// Workers, php-cgi süreç sayısı. 0 ise CPU sayısı.
	Workers int `yaml:"workers,omitempty"`

	// Ini, php.ini yönergeleri.
	Ini map[string]string `yaml:"ini,omitempty"`

	// Extensions, yüklenecek uzantılar.
	Extensions []string `yaml:"extensions,omitempty"`

	// Xdebug, hata ayıklayıcıyı açar.
	Xdebug bool `yaml:"xdebug,omitempty"`
}

// Mail, projenin posta yakalayıcı ayarları.
//
// Yakalayıcı varsayılan olarak açık. Geliştirme ortamındaki en pahalı
// hatalardan biri, test verisindeki gerçek bir adrese gerçekten posta
// gitmesi; DevBox'ın giden postayı öntanımlı olarak tutması bunu
// imkânsız kılıyor. Kapatmak isteyen "disabled: true" yazar.
type Mail struct {
	// Disabled, yakalayıcıyı kapatır.
	Disabled bool `yaml:"disabled,omitempty"`

	// SMTP, dinlenecek SMTP adresi. Boşsa 127.0.0.1:1025.
	SMTP string `yaml:"smtp,omitempty"`

	// Host, posta kutusu arayüzünün alan adı. Boşsa mail.<domain>.
	Host string `yaml:"host,omitempty"`

	// Capacity, bellekte tutulacak en fazla posta. 0 ise varsayılan.
	Capacity int `yaml:"capacity,omitempty"`
}

// CronEntry, zamanlanmış tek bir görev.
type CronEntry struct {
	Schedule string `yaml:"schedule"`
	Run      string `yaml:"run"`
}

// Dir, yapılandırmanın okunduğu proje dizini.
func (c *Config) Dir() string { return c.dir }

// DocumentRoot, belge kökünün mutlak yolu.
func (c *Config) DocumentRoot() string {
	if c.Root == "" {
		return c.dir
	}
	return filepath.Join(c.dir, filepath.FromSlash(c.Root))
}

// MailHost, posta kutusu arayüzünün alan adı.
//
// Öntanımlı mail.<domain>: proje sertifikası *.<domain> joker adını da
// kapsadığı için ayrı sertifika gerekmiyor ve çözücü zaten son eki
// sahipleniyor — yani https://mail.magaza.test ek ayar istemeden açılıyor.
func (c *Config) MailHost() string {
	if c.Mail.Host != "" {
		return c.Mail.Host
	}
	return "mail." + c.Domain
}

// UsesPHP, projenin PHP havuzuna ihtiyaç duyup duymadığını söyler.
func (c *Config) UsesPHP() bool {
	return c.Server == ServerDevBox || c.Server == ServerApache || c.Server == ServerNginx
}

// Load, verilen dizindeki devbox.yaml'ı okur.
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.dir = dir
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse, YAML içeriğini çözer.
//
// Bilinmeyen alanlar hata veriyor: "worker: 4" yazan biri (doğrusu
// "workers") sessizce varsayılanla çalışmak yerine yazım hatasını hemen
// görsün.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("yapılandırma çözülemedi: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server == "" {
		c.Server = ServerDevBox
	}
	if c.FrontController == "" {
		c.FrontController = "index.php"
	}
}

// Validate, yapılandırmanın kullanılabilir olduğunu denetler.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name alanı zorunlu")
	}
	if !validName(c.Name) {
		return fmt.Errorf("geçersiz name %q (harf, rakam, tire ve alt çizgi)", c.Name)
	}
	if c.Domain == "" {
		return fmt.Errorf("domain alanı zorunlu (ör. %s.test)", c.Name)
	}
	for _, d := range append([]string{c.Domain}, c.Aliases...) {
		if !validDomain(d) {
			return fmt.Errorf("geçersiz alan adı %q", d)
		}
	}

	switch c.Server {
	case ServerDevBox, ServerApache, ServerNginx:
	case ServerProxy:
		if c.Proxy == "" {
			return fmt.Errorf("server: proxy için proxy adresi gerekli")
		}
		if !strings.HasPrefix(c.Proxy, "http://") && !strings.HasPrefix(c.Proxy, "https://") {
			return fmt.Errorf("proxy adresi http:// ya da https:// ile başlamalı: %q", c.Proxy)
		}
	default:
		return fmt.Errorf("geçersiz server %q (devbox, apache, nginx ya da proxy)", c.Server)
	}

	// Belge kökü proje dizininin dışına çıkamaz: devbox.yaml depodan
	// geliyor ve klonlayan kişinin makinesinde istediği dizini sunmaya
	// yetkisi olmamalı.
	if c.Root != "" {
		if isAbsoluteAnyPlatform(c.Root) || strings.Contains(filepath.ToSlash(c.Root), "..") {
			return fmt.Errorf("root proje dizininin içinde ve göreli olmalı: %q", c.Root)
		}
	}

	for k := range c.PHP.Ini {
		if strings.ContainsAny(k, "\r\n=") {
			return fmt.Errorf("geçersiz php.ini yönergesi %q", k)
		}
	}
	for _, e := range c.PHP.Extensions {
		if !validName(e) {
			return fmt.Errorf("geçersiz PHP uzantısı %q", e)
		}
	}
	if c.Mail.Host != "" && !validDomain(c.Mail.Host) {
		return fmt.Errorf("geçersiz posta alan adı %q", c.Mail.Host)
	}
	if c.Mail.SMTP != "" {
		if _, _, err := net.SplitHostPort(c.Mail.SMTP); err != nil {
			return fmt.Errorf("geçersiz mail.smtp adresi %q (host:port bekleniyor)", c.Mail.SMTP)
		}
	}
	if c.Mail.Capacity < 0 {
		return fmt.Errorf("mail.capacity negatif olamaz: %d", c.Mail.Capacity)
	}
	for i, entry := range c.Cron {
		if entry.Schedule == "" || entry.Run == "" {
			return fmt.Errorf("%d. cron girdisinde schedule ya da run eksik", i)
		}
		// Zamanlama burada çözülüyor: yazım hatası, görevin sessizce hiç
		// çalışmaması yerine "devbox up"ta hemen görünsün.
		if _, err := cron.Parse(entry.Schedule); err != nil {
			return fmt.Errorf("%d. cron girdisinin zamanlaması: %w", i+1, err)
		}
	}
	return nil
}

// Save, yapılandırmayı dizine yazar.
func (c *Config) Save(dir string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := []byte("# DevBox proje yapılandırması.\n" +
		"# Bu dosyayı depoya ekleyin: ekip arkadaşınız klonlayıp \"devbox up\"\n" +
		"# dediğinde aynı ortam kurulur.\n\n")
	return os.WriteFile(filepath.Join(dir, FileName), append(header, data...), 0o644)
}

// isAbsoluteAnyPlatform, yolun herhangi bir platformun kurallarına göre
// mutlak olup olmadığını söyler.
//
// filepath.IsAbs platforma bağlı: Windows'ta "/etc" mutlak sayılmıyor
// (sürücü harfi yok), Linux'ta "C:/Windows" mutlak sayılmıyor. Ama
// devbox.yaml depodan geliyor ve iki platformda da aynı biçimde
// doğrulanmalı — Linux'ta yazılıp reddedilen bir değer Windows'ta kabul
// edilmemeli. Bu yüzden her iki kuralı da uyguluyoruz.
func isAbsoluteAnyPlatform(p string) bool {
	if p == "" {
		return false
	}
	if p[0] == '/' || p[0] == '\\' {
		return true
	}
	// Windows sürücü öneki: "C:", "c:/proje"
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

func validName(s string) bool {
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
	if s == "" || len(s) > 253 || strings.Contains(s, "..") {
		return false
	}
	body := strings.TrimPrefix(s, "*.")
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}
