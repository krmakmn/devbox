//go:build !windows

package elevate

import "os"

// IsElevated, Unix'te root olup olmadığımızı söyler. Geliştirme ve testler
// için; DevBox'ın hedefi Windows.
func IsElevated() bool { return os.Geteuid() == 0 }

// Relaunch, Windows dışında desteklenmiyor: UAC'nin taşınabilir bir
// karşılığı yok ve sudo'yu bir aracın kendi başına çağırması doğru değil.
func Relaunch(args []string) error { return ErrUnsupported }
