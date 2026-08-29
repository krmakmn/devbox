//go:build !windows

package ports

import (
	"context"
	"fmt"
)

// Windows dışında işletim sistemi port rezervasyonu diye bir kavram yok;
// boş liste dönüyoruz. Bağlama denemesi zaten her platformda doğruyu
// söylüyor.
func readExcludedRanges(context.Context) ([]Range, error) { return nil, nil }

// listenersCommand, portu kimin tuttuğunu gösteren komut.
//
// Windows'un netstat/findstr kalıbı burada işe yaramıyor; kullanıcıya
// çalışmayacak bir komut önermek teşhisi zorlaştırır.
func listenersCommand(port int) string {
	return fmt.Sprintf("ss -ltnp 'sport = :%d'", port)
}
