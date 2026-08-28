// Package paths, DevBox'ın veri dizinini belirler.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir, kök CA'nın, site sertifikalarının ve runtime kurulumlarının
// tutulduğu dizin.
//
// Windows'ta %LOCALAPPDATA%: gezici profil (roaming) kullanan kurumsal
// makinelerde sertifika ve ikili dosyaların ağ üzerinden senkronlanmasını
// istemiyoruz.
func DataDir() string {
	if dir := os.Getenv("DEVBOX_HOME"); dir != "" {
		return dir
	}

	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "DevBox")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "DevBox")
		}
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "devbox")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "devbox")
		}
	}

	// Hiçbiri yoksa çalışma dizininin altına düş; en azından çalışsın.
	return ".devbox"
}

// CertsDir, sertifika deposunun dizini.
func CertsDir() string { return filepath.Join(DataDir(), "certs") }
