//go:build !windows

package trust

import (
	"context"
	"crypto/x509"
	"errors"
	"runtime"
)

// DevBox Windows hedefli. Diğer platformlarda işletim sistemi güven deposuna
// dokunmuyoruz: dağıtımlar arasında yol ve araç farkı büyük, yanlış yere
// yazmak sistemin güven zincirini bozabilir. Geliştirme ve CI için kök,
// testlerde doğrudan havuza eklenerek kullanılıyor.
func installSystem(_ context.Context, cert *x509.Certificate, _ Scope) Result {
	return Result{
		Target: "işletim sistemi güven deposu (" + runtime.GOOS + ")",
		Err:    errors.New("bu platformda desteklenmiyor"),
		Hint:   "kökü elle kurun: Linux'ta /usr/local/share/ca-certificates/ + update-ca-certificates",
	}
}

func systemContains(cert *x509.Certificate, _ Scope) (bool, error) {
	return false, errors.New("trust: bu platformda desteklenmiyor")
}

func systemRemove(cert *x509.Certificate, _ Scope) error {
	return errors.New("trust: bu platformda desteklenmiyor")
}
