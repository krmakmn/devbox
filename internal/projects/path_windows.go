//go:build windows

package projects

import (
	"path/filepath"
	"strings"
)

// normalizeDir, Windows'ta yolu büyük/küçük harf duyarsız karşılaştırma
// için hazırlar.
func normalizeDir(p string) string {
	return strings.ToLower(filepath.Clean(p))
}
