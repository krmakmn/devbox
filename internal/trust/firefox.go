package trust

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// nickname, sertifikanın Firefox'un sertifika yöneticisinde görüneceği ad.
const nickname = "DevBox yerel geliştirme CA"

// installFirefox, kökü bulunan her Firefox profiline kurar.
//
// Firefox, işletim sisteminin güven deposunu kullanmaz; her profilin kendi
// NSS veritabanı (cert9.db) vardır. Bu veritabanına yazmanın desteklenen tek
// yolu NSS'in certutil aracıdır ve Firefox onu birlikte getirmez.
func installFirefox(ctx context.Context, rootPEMPath string, cert *x509.Certificate) []Result {
	profiles := firefoxProfiles(firefoxProfileRoots())
	if len(profiles) == 0 {
		// Firefox kurulu değilse bu bir hata değil.
		return nil
	}

	tool, toolErr := findCertutil()
	results := make([]Result, 0, len(profiles))
	for _, profile := range profiles {
		res := Result{Target: "Firefox — " + filepath.Base(profile)}
		if toolErr != nil {
			res.Err = toolErr
			res.Hint = certutilHint()
			results = append(results, res)
			continue
		}

		cmd := exec.CommandContext(ctx, tool, certutilArgs(profile, rootPEMPath)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Err = fmt.Errorf("certutil başarısız: %w: %s", err, strings.TrimSpace(string(out)))
			res.Hint = certutilHint()
		} else {
			res.Installed = true
		}
		results = append(results, res)
	}
	return results
}

// certutilArgs, NSS certutil çağrısının argümanlarını üretir.
//
// -t C,, güven bayrağı: sertifika yalnız sunucu kimliği (TLS) için güvenilir
// olsun; e-posta ve kod imzalama için değil. Yerel bir CA'ya gereğinden fazla
// yetki vermemek önemli.
func certutilArgs(profileDir, certPath string) []string {
	return []string{
		"-A",
		"-d", "sql:" + profileDir,
		"-t", "C,,",
		"-n", nickname,
		"-i", certPath,
	}
}

// firefoxProfileRoots, Firefox profillerinin aranacağı dizinleri döner.
func firefoxProfileRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	switch runtime.GOOS {
	case "windows":
		var roots []string
		if appData := os.Getenv("APPDATA"); appData != "" {
			roots = append(roots,
				filepath.Join(appData, "Mozilla", "Firefox", "Profiles"))
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			roots = append(roots,
				filepath.Join(local, "Mozilla", "Firefox", "Profiles"))
		}
		return roots
	case "darwin":
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles")}
	default:
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, ".mozilla", "firefox"),
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
		}
	}
}

// firefoxProfiles, verilen kök dizinlerin altındaki gerçek profilleri bulur.
//
// Bir dizini profil sayma ölçütü, içinde NSS veritabanının bulunmasıdır.
// Firefox'un profiles.ini dosyasını ayrıştırmak yerine bunu tercih ediyoruz:
// ini biçimi sürümler arasında değişti, veritabanı dosyasının adı değişmedi.
func firefoxProfiles(roots []string) []string {
	var profiles []string
	seen := map[string]bool{}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if !hasNSSDatabase(dir) || seen[dir] {
				continue
			}
			seen[dir] = true
			profiles = append(profiles, dir)
		}
	}
	return profiles
}

func hasNSSDatabase(dir string) bool {
	for _, name := range []string{"cert9.db", "key4.db"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// findCertutil, NSS certutil aracını arar.
func findCertutil() (string, error) {
	if path, err := exec.LookPath("certutil"); err == nil {
		if runtime.GOOS == "windows" && isWindowsCertutil(path) {
			// Windows'un kendi certutil.exe'si aynı adı taşır ama NSS
			// veritabanıyla ilgisi yoktur; onu çalıştırmak anlaşılmaz
			// hatalar üretir.
			return "", errors.New("PATH'teki certutil Windows'un kendi aracı, NSS certutil değil")
		}
		return path, nil
	}
	return "", errors.New("NSS certutil bulunamadı")
}

// isWindowsCertutil, bulunan aracın Windows'un System32'deki certutil'i olup
// olmadığını yola bakarak anlar.
//
// Ters bölüyü elle çeviriyoruz: filepath.ToSlash Windows dışında hiçbir şey
// yapmaz, oysa bu kararın Windows yollarını her platformda aynı okuması
// gerekiyor (yoksa yalnız Windows'ta doğru çalışan, başka yerde sessizce
// yanlış bir dal olur).
func isWindowsCertutil(path string) bool {
	lower := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	return strings.Contains(lower, "/windows/system32/") ||
		strings.Contains(lower, "/windows/syswow64/")
}

func certutilHint() string {
	if runtime.GOOS == "windows" {
		return "NSS araçlarını kurun (ör. scoop install nss) ya da Firefox'ta " +
			"Ayarlar → Gizlilik ve Güvenlik → Sertifikaları Görüntüle → İçe Aktar ile kökü elle ekleyin"
	}
	return "NSS araçlarını kurun (Debian/Ubuntu: apt install libnss3-tools)"
}
