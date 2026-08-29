// Package scaffold, yeni proje iskeletleri kurar.
//
// # Neden çerçevelerin kendi araçlarını çağırıyoruz
//
// Bir "laravel şablonu" tutmak, Laravel'in dosya düzenini bu depoya
// kopyalamak demek olurdu. O kopya ilk günden eskimeye başlar: Laravel
// sürüm atlar, dosya düzeni değişir, kullanıcı DevBox'ın bir yıl önceki
// iskeletiyle başlar ve sorunu DevBox'a yazar. Bunun yerine çerçevenin
// kendi kurucusu çağrılıyor (composer create-project, npm create). DevBox'ın
// işi ortam kurmak; çerçeve dağıtmak değil.
//
// Yalnız kendi ürettiğimiz iki şablon var — düz PHP ve statik site. Onların
// "çerçevesi" olmadığı için kopyalanacak bir şey de yok: iki dosya.
//
// # Araç yoksa ne oluyor
//
// Sessizce boş dizin bırakmak yerine, hangi aracın eksik olduğu ve nasıl
// kurulacağı söyleniyor. Kullanıcı "devbox new laravel" deyip boş bir
// klasörle kalırsa hatayı DevBox'ta arar.
package scaffold

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Template, bir proje şablonu.
type Template struct {
	// Name, komut satırında yazılan ad ("laravel").
	Name string

	// Description, listede görünen açıklama.
	Description string

	// Tool, gereken çalıştırılabilir ("composer"). Boşsa araç gerekmiyor.
	Tool string

	// InstallHint, araç yoksa gösterilecek yol.
	InstallHint string

	// args, hedef dizinin adı verildiğinde çalıştırılacak komutu üretir.
	//
	// Mutlak yol değil ad: kurucuların çoğu (create-vite, create-next-app)
	// verilen yolu çalışma dizinine ekliyor, yani mutlak yol verildiğinde
	// proje /tmp/yeni/tmp/yeni/arayuz gibi bir yere kuruluyor. Gerçek
	// kurucuyla denenince çıktı. Komut hedefin üst dizininde çalıştırılıp
	// ad veriliyor; bu her kurucuda aynı sonucu veriyor.
	args func(name string) []string

	// files, araç gerektirmeyen şablonların yazacağı dosyalar.
	files map[string]string
}

// NeedsTool, şablonun dış bir araca ihtiyacı olup olmadığını söyler.
func (t Template) NeedsTool() bool { return t.Tool != "" }

var templates = map[string]Template{
	"laravel": {
		Name:        "laravel",
		Description: "Laravel (composer create-project)",
		Tool:        "composer",
		InstallHint: "https://getcomposer.org/download/",
		args: func(name string) []string {
			return []string{"create-project", "laravel/laravel", name}
		},
	},
	"symfony": {
		Name:        "symfony",
		Description: "Symfony iskeleti (composer create-project)",
		Tool:        "composer",
		InstallHint: "https://getcomposer.org/download/",
		args: func(name string) []string {
			return []string{"create-project", "symfony/skeleton", name}
		},
	},
	"next": {
		Name:        "next",
		Description: "Next.js (create-next-app)",
		Tool:        "npx",
		InstallHint: "https://nodejs.org/",
		args: func(name string) []string {
			return []string{"--yes", "create-next-app@latest", name}
		},
	},
	"vite": {
		Name:        "vite",
		Description: "Vite (npm create vite)",
		Tool:        "npm",
		InstallHint: "https://nodejs.org/",
		args: func(name string) []string {
			return []string{"create", "vite@latest", name, "--", "--template", "vanilla"}
		},
	},
	"wordpress": {
		Name:        "wordpress",
		Description: "WordPress (wp-cli ile çekirdek indirilir)",
		Tool:        "wp",
		InstallHint: "https://wp-cli.org/#installing",
		args: func(name string) []string {
			return []string{"core", "download", "--path=" + name}
		},
	},
	"php": {
		Name:        "php",
		Description: "Düz PHP (public/ dizinli)",
		files: map[string]string{
			"public/index.php": phpIndex,
			".gitignore":       "/vendor/\n/.idea/\n/devbox.local.yaml\n",
		},
	},
	"static": {
		Name:        "static",
		Description: "Statik site",
		files: map[string]string{
			"index.html": staticIndex,
		},
	},
}

const phpIndex = `<?php

// DevBox ile üretilmiş başlangıç dosyası.
// Belge kökü public/ — devbox.yaml'daki root alanı bunu gösteriyor.

echo '<!doctype html><meta charset="utf-8">';
echo '<h1>Çalışıyor</h1>';
printf('<p>PHP %s</p>', htmlspecialchars(PHP_VERSION, ENT_QUOTES, 'UTF-8'));
`

const staticIndex = `<!doctype html>
<html lang="tr">
<meta charset="utf-8">
<title>Yeni site</title>
<h1>Çalışıyor</h1>
<p>Bu dosyayı düzenleyerek başlayın: <code>index.html</code></p>
`

// Templates, şablonları ada göre sıralı döner.
func Templates() []Template {
	out := make([]Template, 0, len(templates))
	for _, t := range templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get, şablonu ada göre döner.
func Get(name string) (Template, error) {
	t, ok := templates[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		names := make([]string, 0, len(templates))
		for _, t := range Templates() {
			names = append(names, t.Name)
		}
		return Template{}, fmt.Errorf("scaffold: bilinmeyen şablon %q (%s)",
			name, strings.Join(names, ", "))
	}
	return t, nil
}

// Options, iskelet kurma ayarları.
type Options struct {
	// Dir, projenin kurulacağı dizin.
	Dir string

	// Output, aracın çıktısının yazılacağı yer. Boşsa yutuluyor.
	Output *os.File
}

// Create, şablonu verilen dizine kurar.
//
// Dizin varsa ve boş değilse hata veriyor: var olan bir projenin üstüne
// yazmak geri alınamaz.
func (t Template) Create(ctx context.Context, opts Options) error {
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	if err := ensureEmptyDir(dir); err != nil {
		return err
	}

	if !t.NeedsTool() {
		return t.writeFiles(dir)
	}

	tool, err := exec.LookPath(t.Tool)
	if err != nil {
		return fmt.Errorf("scaffold: %q şablonu için %s gerekiyor ama bulunamadı.\n  Kurulum: %s",
			t.Name, t.Tool, t.InstallHint)
	}

	// Kurucular dizini kendileri oluşturuyor; boş bir dizin bırakırsak
	// bazıları "dizin dolu" diye şikâyet ediyor.
	os.Remove(dir)

	cmd := exec.CommandContext(ctx, tool, t.args(filepath.Base(dir))...)
	cmd.Dir = filepath.Dir(dir)
	if opts.Output != nil {
		cmd.Stdout = opts.Output
		cmd.Stderr = opts.Output
	}
	// Etkileşimli soru sormasınlar: DevBox'ın çağrısı toplu iş.
	cmd.Env = append(os.Environ(), "CI=1", "COMPOSER_NO_INTERACTION=1")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scaffold: %s başarısız oldu: %w", t.Tool, err)
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("scaffold: %s çalıştı ama %s oluşmadı", t.Tool, dir)
	}
	return nil
}

// writeFiles, araç gerektirmeyen şablonun dosyalarını yazar.
func (t Template) writeFiles(dir string) error {
	for name, content := range t.files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ensureEmptyDir, dizinin var olmadığını ya da boş olduğunu doğrular.
func ensureEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("scaffold: %s boş değil; var olan bir projenin üstüne yazılmaz", dir)
	}
	return nil
}
