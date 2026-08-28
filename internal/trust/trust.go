// Package trust, DevBox'ın kök sertifikasını işletim sisteminin ve
// tarayıcıların güven depolarına kurar.
//
// Buradaki asıl mesele Firefox'tur. Chrome ve Edge Windows'un güven deposunu
// kullanır; Firefox kullanmaz, kendi NSS veritabanını taşır. Laragon'un
// atladığı nokta tam olarak bu ve "sertifika kurdum ama Firefox'ta hâlâ uyarı
// veriyor" şikâyetlerinin tek sebebi.
//
// Kurulum en iyi çabadır: bir hedef başarısız olursa diğerleri denenir ve her
// biri için ne yapılması gerektiğini söyleyen bir sonuç döner. Sessizce
// yarım kalmak, kullanıcının saatlerce yanlış yerde hata aramasına yol açar.
package trust

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Result, tek bir güven deposu için kurulum sonucudur.
type Result struct {
	// Target, insan tarafından okunabilir hedef adı.
	Target string

	// Installed, kök bu hedefte güvenilir durumdaysa true.
	Installed bool

	// Err, kurulum denenip başarısız olduysa hata.
	Err error

	// Hint, kullanıcının elle ne yapması gerektiği (Err doluysa).
	Hint string
}

func (r Result) String() string {
	switch {
	case r.Installed:
		return "✓ " + r.Target
	case r.Err != nil && r.Hint != "":
		return fmt.Sprintf("✗ %s: %v\n  → %s", r.Target, r.Err, r.Hint)
	case r.Err != nil:
		return fmt.Sprintf("✗ %s: %v", r.Target, r.Err)
	default:
		return "- " + r.Target + " (atlandı)"
	}
}

// Install, PEM dosyasındaki kök sertifikayı bulunabilen tüm güven depolarına
// kurar. Hiçbiri başarılı olmasa bile sonuç listesi döner; çağıran hangi
// hedefin neden başarısız olduğunu görebilsin.
func Install(rootPEMPath string) ([]Result, error) {
	cert, err := loadCert(rootPEMPath)
	if err != nil {
		return nil, err
	}

	results := []Result{installSystem(cert)}
	results = append(results, installFirefox(rootPEMPath, cert)...)
	return results, nil
}

// IsInstalled, kökün işletim sistemi güven deposunda olup olmadığını söyler.
func IsInstalled(rootPEMPath string) (bool, error) {
	cert, err := loadCert(rootPEMPath)
	if err != nil {
		return false, err
	}
	return systemContains(cert)
}

// Uninstall, kökü işletim sistemi güven deposundan kaldırır.
func Uninstall(rootPEMPath string) error {
	cert, err := loadCert(rootPEMPath)
	if err != nil {
		return err
	}
	return systemRemove(cert)
}

func loadCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trust: kök sertifika okunamadı: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("trust: dosya geçerli bir sertifika PEM'i değil")
	}
	return x509.ParseCertificate(block.Bytes)
}
