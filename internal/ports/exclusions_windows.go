//go:build windows

package ports

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// readExcludedRanges, Windows'un rezerve ettiği port aralıklarını okur.
//
// netsh çağrısı yönetici hakkı istemiyor; yalnız okuma.
func readExcludedRanges(ctx context.Context) ([]Range, error) {
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "netsh", "int", "ipv4", "show", "excludedportrange", "protocol=tcp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ports: rezerve aralıklar okunamadı: %w", err)
	}
	return ParseExcludedRanges(string(out)), nil
}
