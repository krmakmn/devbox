//go:build !windows

package ports

import "context"

// Windows dışında işletim sistemi port rezervasyonu diye bir kavram yok;
// boş liste dönüyoruz. Bağlama denemesi zaten her platformda doğruyu
// söylüyor.
func readExcludedRanges(context.Context) ([]Range, error) { return nil, nil }
