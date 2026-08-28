package runtime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxEntrySize, tek bir arşiv girdisi için üst sınır (2 GiB).
//
// Sıkıştırma bombalarına karşı: birkaç kilobaytlık bir arşiv, açıldığında
// diski doldurabilir.
const maxEntrySize = 2 << 30

// extract, arşivi hedef dizine açar.
func extract(archivePath, destDir, format, stripPrefix string) error {
	switch format {
	case "zip":
		return extractZip(archivePath, destDir, stripPrefix)
	case "tar.gz":
		return extractTarGz(archivePath, destDir, stripPrefix)
	default:
		return fmt.Errorf("runtime: bilinmeyen arşiv biçimi %q", format)
	}
}

// safeEntryPath, arşiv girdisinin hedef dizindeki güvenli karşılığını döner.
//
// "Zip Slip" saldırısı: arşivdeki bir girdinin adı "../../Windows/System32/..."
// olursa, naif bir açıcı dosyayı hedef dizinin dışına yazar. Arşivi biz
// üretmediğimiz için (uzaktan indiriliyor) bu denetim zorunlu.
func safeEntryPath(destDir, name, stripPrefix string) (string, bool) {
	// Kaçmaya çalışan girdiyi temizleyip kurtarmıyoruz, reddediyoruz.
	// Meşru bir PHP ya da Node dağıtımında ".." veya mutlak yol içeren
	// girdi bulunmaz; böyle bir şey görüyorsak ya arşiv kötü niyetli ya da
	// manifest yanlış yeri gösteriyor. İkisi de sessizce düzeltilecek
	// değil, durup bildirilecek durumlar.
	if escapesDest(name) {
		return "", false
	}

	// Arşiv içinde her zaman eğik bölü kullanılır; ters bölü kullanan bozuk
	// üreticilere karşı ikisini de normalleştiriyoruz.
	clean := path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", false
	}

	if stripPrefix != "" {
		prefix := strings.Trim(strings.ReplaceAll(stripPrefix, `\`, "/"), "/") + "/"
		if !strings.HasPrefix(clean, prefix) {
			// Önekin dışındaki girdiler atlanır.
			return "", false
		}
		clean = strings.TrimPrefix(clean, prefix)
		if clean == "" {
			return "", false
		}
	}

	target := filepath.Join(destDir, filepath.FromSlash(clean))
	// İkinci savunma hattı: sonuç gerçekten hedefin içinde mi?
	if target != destDir && !strings.HasPrefix(target, destDir+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

func extractZip(archivePath, destDir, stripPrefix string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("runtime: zip açılamadı: %w", err)
	}
	defer r.Close()

	for _, entry := range r.File {
		target, ok := safeEntryPath(destDir, entry.Name, stripPrefix)
		if !ok {
			if escapesDest(entry.Name) {
				return fmt.Errorf("runtime: arşiv hedef dizinin dışına yazmaya çalışıyor: %q", entry.Name)
			}
			continue
		}

		info := entry.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// Zip'te sembolik bağ nadirdir ve Windows'ta ayrıcalık ister;
			// atlıyoruz.
			continue
		}
		if entry.UncompressedSize64 > maxEntrySize {
			return fmt.Errorf("runtime: arşiv girdisi çok büyük: %q (%d bayt)", entry.Name, entry.UncompressedSize64)
		}

		if err := writeEntry(target, info.Mode(), func() (io.ReadCloser, error) {
			return entry.Open()
		}); err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archivePath, destDir, stripPrefix string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("runtime: gzip açılamadı: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("runtime: tar okunamadı: %w", err)
		}

		target, ok := safeEntryPath(destDir, header.Name, stripPrefix)
		if !ok {
			if escapesDest(header.Name) {
				return fmt.Errorf("runtime: arşiv hedef dizinin dışına yazmaya çalışıyor: %q", header.Name)
			}
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size > maxEntrySize {
				return fmt.Errorf("runtime: arşiv girdisi çok büyük: %q", header.Name)
			}
			err := writeEntry(target, os.FileMode(header.Mode).Perm(), func() (io.ReadCloser, error) {
				return io.NopCloser(io.LimitReader(tr, header.Size)), nil
			})
			if err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Bağın hedefi de dizinin dışına çıkabilir; çözmek yerine
			// atlıyoruz. Windows dağıtımlarında bağ kullanılmıyor.
			continue
		default:
			continue
		}
	}
}

// escapesDest, girdinin dizin dışına çıkmaya çalışıp çalışmadığını söyler.
// (Önek eşleşmediği için atlanan girdilerden ayırt etmek için.)
func escapesDest(name string) bool {
	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return true
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return true
		}
	}
	// Windows sürücü öneki: "C:/..." da mutlak yoldur.
	if len(normalized) > 1 && normalized[1] == ':' {
		return true
	}
	return false
}

func writeEntry(target string, mode os.FileMode, open func() (io.ReadCloser, error)) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := open()
	if err != nil {
		return err
	}
	defer src.Close()

	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, io.LimitReader(src, maxEntrySize)); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
