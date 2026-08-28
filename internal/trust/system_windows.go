//go:build windows

package trust

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Windows'ta kök, kullanıcının ROOT deposuna kurulur. Makine geneli
// (LOCAL_MACHINE) yönetici hakkı ister; geliştirme ortamı için kullanıcı
// deposu hem yeter hem de yükseltme istemez. Chrome ve Edge bu depoyu okur.
const (
	certStoreAddReplaceExisting = 3
	certCompareShift            = 16
	certCompareAny              = 0
	certFindAny                 = certCompareAny << certCompareShift
)

var (
	crypt32                            = syscall.NewLazyDLL("crypt32.dll")
	procCertDeleteCertificateFromStore = crypt32.NewProc("CertDeleteCertificateFromStore")
)

func openRootStore() (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString("ROOT")
	if err != nil {
		return 0, err
	}
	store, err := syscall.CertOpenSystemStore(0, name)
	if err != nil {
		return 0, fmt.Errorf("trust: ROOT deposu açılamadı: %w", err)
	}
	return store, nil
}

// installSystem, kökü kullanıcının ROOT deposuna ekler.
//
// Windows bu sırada onay penceresi gösterir ve çağrı yanıtlanana kadar
// döner değil. Masaüstü oturumu yoksa pencere hiç gösterilemez ve çağrı
// süresiz bloke olur; bu yüzden ekleme ayrı bir goroutine'de yapılıp
// bağlamla sınırlanıyor. Goroutine kendi hâline bırakılıyor: syscall'ı
// iptal etmenin yolu yok, ama süreç sonlandığında o da gider.
func installSystem(ctx context.Context, cert *x509.Certificate) Result {
	res := Result{Target: "Windows güven deposu (Chrome, Edge)"}

	done := make(chan error, 1)
	go func() { done <- addToRootStore(cert) }()

	select {
	case err := <-done:
		if err != nil {
			res.Err = err
			res.Hint = "certutil -addstore -user Root <kök.crt> ile elle kurabilirsiniz"
			return res
		}
		res.Installed = true
		return res
	case <-ctx.Done():
		res.Err = errors.New("Windows onay penceresi yanıtlanmadı")
		res.Hint = "Kök sertifika eklerken Windows onay ister. Açılan pencereyi onaylayın; " +
			"masaüstü oturumu yoksa (servis, CI, uzak oturum) bu yol kullanılamaz — " +
			"certutil -addstore -user Root <kök.crt> deneyin"
		return res
	}
}

func addToRootStore(cert *x509.Certificate) error {
	store, err := openRootStore()
	if err != nil {
		return err
	}
	defer syscall.CertCloseStore(store, 0)

	certCtx, err := syscall.CertCreateCertificateContext(
		syscall.X509_ASN_ENCODING|syscall.PKCS_7_ASN_ENCODING,
		&cert.Raw[0], uint32(len(cert.Raw)),
	)
	if err != nil {
		return fmt.Errorf("trust: sertifika bağlamı oluşturulamadı: %w", err)
	}
	defer syscall.CertFreeCertificateContext(certCtx)

	if err := syscall.CertAddCertificateContextToStore(store, certCtx, certStoreAddReplaceExisting, nil); err != nil {
		return fmt.Errorf("trust: sertifika depoya eklenemedi: %w", err)
	}
	return nil
}

func systemContains(cert *x509.Certificate) (bool, error) {
	store, err := openRootStore()
	if err != nil {
		return false, err
	}
	defer syscall.CertCloseStore(store, 0)

	found, ctx, err := findInStore(store, cert)
	if ctx != nil {
		syscall.CertFreeCertificateContext(ctx)
	}
	return found, err
}

// findInStore, depoyu tarayıp sertifikayı ham baytlarıyla arar.
//
// Parmak izi karşılaştırmak yerine tam DER karşılaştırıyoruz: aynı konuya
// sahip eski bir DevBox kökü depoda kalmış olabilir, onu "kurulu" saymak
// yanlış olur.
func findInStore(store syscall.Handle, cert *x509.Certificate) (bool, *syscall.CertContext, error) {
	var prev *syscall.CertContext
	for {
		ctx, err := syscall.CertEnumCertificatesInStore(store, prev)
		if err != nil {
			if errno, ok := err.(syscall.Errno); ok && errno == syscall.Errno(0x80092004) {
				// CRYPT_E_NOT_FOUND: numaralandırma bitti.
				return false, nil, nil
			}
			return false, nil, err
		}
		if ctx == nil {
			return false, nil, nil
		}
		encoded := unsafe.Slice(ctx.EncodedCert, ctx.Length)
		if bytes.Equal(encoded, cert.Raw) {
			return true, ctx, nil
		}
		prev = ctx
	}
}

func systemRemove(cert *x509.Certificate) error {
	store, err := openRootStore()
	if err != nil {
		return err
	}
	defer syscall.CertCloseStore(store, 0)

	found, ctx, err := findInStore(store, cert)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	// CertDeleteCertificateFromStore bağlamı her hâlükârda serbest bırakır;
	// ayrıca CertFreeCertificateContext çağırmak çift serbest bırakma olur.
	ret, _, callErr := procCertDeleteCertificateFromStore.Call(uintptr(unsafe.Pointer(ctx)))
	if ret == 0 {
		return fmt.Errorf("trust: sertifika depodan silinemedi: %w", callErr)
	}
	return nil
}
