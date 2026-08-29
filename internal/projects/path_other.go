//go:build !windows

package projects

import "path/filepath"

// normalizeDir, Unix'te yalnız yolu sadeleştirir: dosya sistemi
// büyük/küçük harfe duyarlı.
func normalizeDir(p string) string {
	return filepath.Clean(p)
}
