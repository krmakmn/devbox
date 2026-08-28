package webserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Driver, bir web sunucusunun yapılandırmasını üretir ve yönetir.
type Driver interface {
	// Name, sürücünün adı ("apache", "nginx").
	Name() string

	// Render, siteler için yapılandırma metnini üretir.
	Render(sites []Site) (string, error)

	// Write, yapılandırmayı diske atomik olarak yazar.
	Write(path string, sites []Site) error

	// Validate, yapılandırmayı sunucunun kendi söz dizimi denetimiyle
	// sınar (httpd -t, nginx -t).
	Validate(ctx context.Context, configPath string) error

	// Reload, çalışan sunucuya yapılandırmayı yeniden okutur.
	Reload(ctx context.Context) error
}

// ErrNoBinary, sunucunun çalıştırılabiliri tanımlı değilse döner.
var ErrNoBinary = errors.New("webserver: sunucu çalıştırılabiliri tanımlı değil")

// Apply, yapılandırmayı yazar, doğrular ve yükler.
//
// Sıra önemli: önce yaz, sonra sunucunun kendi denetimiyle doğrula, ancak
// sonra yükle. Doğrulama başarısız olursa eski dosya geri konur — bozuk bir
// yapılandırmayla yeniden yükleme, çalışan siteleri de düşürür.
func Apply(ctx context.Context, d Driver, configPath string, sites []Site) error {
	backup, hadPrevious, err := readIfExists(configPath)
	if err != nil {
		return err
	}

	if err := d.Write(configPath, sites); err != nil {
		return err
	}

	if err := d.Validate(ctx, configPath); err != nil {
		if errors.Is(err, ErrNoBinary) {
			// Sunucu kurulu değil: yapılandırma yazıldı, doğrulanamadı.
			// Bu bir hata değil; kurulumdan önce yapılandırma üretmek
			// meşru bir senaryo.
			return nil
		}
		// Eski hâline döndür ki çalışan sunucu bozuk dosyayı okumasın.
		if hadPrevious {
			os.WriteFile(configPath, backup, 0o644)
		} else {
			os.Remove(configPath)
		}
		return fmt.Errorf("%s yapılandırması reddedildi, değişiklik geri alındı: %w", d.Name(), err)
	}

	if err := d.Reload(ctx); err != nil && !errors.Is(err, ErrNoBinary) {
		return err
	}
	return nil
}

func readIfExists(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// writeConfig, yapılandırmayı atomik olarak yazar.
//
// Yarım yazılmış bir yapılandırma dosyası, sunucunun açılışta anlaşılmaz bir
// hatayla ölmesi demek. Ayrıca satır sonu LF ve BOM yok: Apache, BOM'lu bir
// yapılandırma dosyasında "Invalid command" gibi tamamen yanıltıcı bir hata
// verir ve sebebini bulmak saatler alır.
func writeConfig(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(normalized), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// runCommand, sunucunun kendi aracını çalıştırır ve çıktısını hataya ekler.
func runCommand(ctx context.Context, binary string, args ...string) error {
	if binary == "" {
		return ErrNoBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		if _, statErr := os.Stat(binary); statErr != nil {
			return fmt.Errorf("%w: %s", ErrNoBinary, binary)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Sunucunun kendi mesajı olmadan "exit status 1" hiçbir şey
		// anlatmıyor; hangi satırın hatalı olduğunu o söylüyor.
		return fmt.Errorf("%s %s: %w\n%s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
