package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Detection, bir dizinde tanınan proje türü.
type Detection struct {
	// Framework, insan tarafından okunabilir çerçeve adı.
	Framework string

	// Config, önerilen yapılandırma.
	Config *Config

	// Notes, kullanıcıya gösterilecek açıklamalar (neden bu sunucu
	// seçildi, ne yapması gerekebilir).
	Notes []string
}

// Detect, dizindeki projeyi tanır ve makul bir yapılandırma önerir.
//
// Hiçbir kural eşleşmezse statik site varsayılıyor: yanlış tahmin edip
// çalışmayan bir yapılandırma üretmektense en basitini önermek daha iyi.
func Detect(dir string) *Detection {
	name := suggestName(filepath.Base(dir))
	base := &Config{
		Name:            name,
		Domain:          name + ".test",
		Server:          ServerDevBox,
		FrontController: "index.php",
		dir:             dir,
	}

	composer := readComposer(dir)

	switch {
	case exists(dir, "artisan") && composer.requires("laravel/framework"):
		cfg := *base
		cfg.Root = "public"
		return &Detection{
			Framework: "Laravel",
			Config:    &cfg,
			Notes: []string{
				"Belge kökü public/ olarak ayarlandı.",
				"Kuyruk işçisi için processes bölümüne ekleyebilirsiniz: " +
					"queue: php artisan queue:work",
			},
		}

	case composer.requires("symfony/framework-bundle"), exists(dir, "bin/console") && exists(dir, "public/index.php"):
		cfg := *base
		cfg.Root = "public"
		return &Detection{Framework: "Symfony", Config: &cfg}

	case exists(dir, "wp-config.php"), exists(dir, "wp-load.php"), exists(dir, "wp-settings.php"):
		cfg := *base
		// WordPress kalıcı bağlantıları .htaccess yeniden yazma
		// kurallarına dayanıyor; Apache dışında ek yapılandırma gerekir.
		cfg.Server = ServerApache
		return &Detection{
			Framework: "WordPress",
			Config:    &cfg,
			Notes: []string{
				"Sunucu olarak Apache seçildi: WordPress kalıcı bağlantıları " +
					".htaccess yeniden yazma kurallarına dayanıyor.",
			},
		}

	case exists(dir, "manage.py"):
		cfg := *base
		cfg.Server = ServerProxy
		cfg.Proxy = "http://127.0.0.1:8000"
		return &Detection{
			Framework: "Django",
			Config:    &cfg,
			Notes: []string{
				"Django kendi geliştirme sunucusunu çalıştırıyor; istekler ona iletilecek.",
				"python manage.py runserver komutunu ayrıca çalıştırmanız gerekiyor.",
			},
		}

	case hasNodeDependency(dir, "next"):
		return nodeDetection(base, "Next.js", "http://127.0.0.1:3000", "npm run dev")
	case hasNodeDependency(dir, "nuxt"):
		return nodeDetection(base, "Nuxt", "http://127.0.0.1:3000", "npm run dev")
	case hasNodeDependency(dir, "vite"):
		return nodeDetection(base, "Vite", "http://127.0.0.1:5173", "npm run dev")

	case exists(dir, "public/index.php"):
		cfg := *base
		cfg.Root = "public"
		return &Detection{Framework: "PHP (public/ dizinli)", Config: &cfg}

	case exists(dir, "index.php"):
		return &Detection{Framework: "PHP", Config: base}

	default:
		cfg := *base
		return &Detection{
			Framework: "statik site",
			Config:    &cfg,
			Notes: []string{
				"Tanınan bir çerçeve bulunamadı; statik dosya sunumu varsayıldı.",
				"Yanlışsa devbox.yaml içindeki server ve root alanlarını düzenleyin.",
			},
		}
	}
}

func nodeDetection(base *Config, framework, target, command string) *Detection {
	cfg := *base
	cfg.Server = ServerProxy
	cfg.Proxy = target
	cfg.Processes = map[string]string{"dev": command}
	return &Detection{
		Framework: framework,
		Config:    &cfg,
		Notes: []string{
			framework + " kendi geliştirme sunucusunu çalıştırıyor; istekler " + target + " adresine iletilecek.",
			"Geliştirme sunucusu processes bölümüne eklendi.",
		},
	}
}

// composerFile, composer.json'un ilgilendiğimiz kısmı.
type composerFile struct {
	Require    map[string]string `json:"require"`
	RequireDev map[string]string `json:"require-dev"`
}

func (c composerFile) requires(pkg string) bool {
	if _, ok := c.Require[pkg]; ok {
		return true
	}
	_, ok := c.RequireDev[pkg]
	return ok
}

func readComposer(dir string) composerFile {
	var out composerFile
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return out
	}
	// Bozuk composer.json algılamayı durdurmamalı; diğer kurallar denensin.
	json.Unmarshal(data, &out)
	return out
}

// hasNodeDependency, package.json'da paketin geçip geçmediğine bakar.
func hasNodeDependency(dir, pkg string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkgFile struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkgFile) != nil {
		return false
	}
	if _, ok := pkgFile.Dependencies[pkg]; ok {
		return true
	}
	_, ok := pkgFile.DevDependencies[pkg]
	return ok
}

func exists(dir, rel string) bool {
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil && (info.Mode().IsRegular() || info.IsDir())
}

// suggestName, dizin adından geçerli bir proje adı üretir.
func suggestName(dir string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(dir) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		case unicode.IsLetter(r):
			// Alan adında ASCII olmayan harf kullanmak IDN gerektiriyor;
			// proje adını sade tutup kullanıcıya düzeltme şansı bırakıyoruz.
			sb.WriteRune('-')
		case r == ' ' || r == '.':
			sb.WriteRune('-')
		}
	}
	name := strings.Trim(sb.String(), "-")
	if name == "" {
		return "proje"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
