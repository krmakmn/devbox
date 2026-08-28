//go:build !windows

package nrpt

import "errors"

// NRPT Windows'a özgü. Diğer platformlarda çağrılar açık bir hatayla döner;
// betik üretimi ve doğrulama mantığı yine de her yerde test edilebilir.

var errUnsupported = errors.New("nrpt: yalnız Windows'ta destekleniyor")

func Supported() bool { return false }

func Add(r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return errUnsupported
}

func Remove(namespace string) error { return errUnsupported }

func List() ([]Rule, error) { return nil, errUnsupported }
