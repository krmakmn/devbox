// DevBox'ın ACME sunucusunu bağımsız bir istemciyle (lego) sınayan program.
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

type kullanici struct {
	eposta  string
	kayit   *registration.Resource
	anahtar crypto.PrivateKey
}

func (u *kullanici) GetEmail() string                        { return u.eposta }
func (u *kullanici) GetRegistration() *registration.Resource { return u.kayit }
func (u *kullanici) GetPrivateKey() crypto.PrivateKey        { return u.anahtar }

func main() {
	dizinURL := os.Args[1]
	alan := os.Args[2]
	httpPort := os.Args[3]

	anahtar, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	u := &kullanici{eposta: "kerim@devbox.test", anahtar: anahtar}

	cfg := lego.NewConfig(u)
	cfg.CADirURL = dizinURL
	cfg.Certificate.KeyType = certcrypto.RSA2048

	istemci, err := lego.NewClient(cfg)
	if err != nil {
		panic(err)
	}
	if err := istemci.Challenge.SetHTTP01Provider(
		http01.NewProviderServer("127.0.0.1", httpPort)); err != nil {
		panic(err)
	}

	kayit, err := istemci.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		panic(fmt.Errorf("hesap açılamadı: %w", err))
	}
	u.kayit = kayit
	fmt.Println("hesap açıldı")

	res, err := istemci.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{alan},
		Bundle:  true,
	})
	if err != nil {
		panic(fmt.Errorf("sertifika alınamadı: %w", err))
	}

	blok, _ := pemDecode(res.Certificate)
	sertifika, err := x509.ParseCertificate(blok)
	if err != nil {
		panic(err)
	}
	fmt.Println("SERTİFİKA ALINDI")
	fmt.Println("  konu     :", sertifika.Subject.CommonName)
	fmt.Println("  SAN      :", sertifika.DNSNames)
	fmt.Println("  veren    :", sertifika.Issuer.CommonName)
	fmt.Println("  geçerlilik:", sertifika.NotAfter.Sub(sertifika.NotBefore))
	os.WriteFile("alinan.crt", res.Certificate, 0o644)
}

func pemDecode(data []byte) ([]byte, []byte) {
	const bas = "-----BEGIN CERTIFICATE-----\n"
	const son = "-----END CERTIFICATE-----"
	i := indexOf(string(data), bas)
	j := indexOf(string(data), son)
	if i < 0 || j < 0 {
		panic("PEM bulunamadı")
	}
	der := decodeBase64(string(data[i+len(bas) : j]))
	return der, nil
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func decodeBase64(s string) []byte {
	var temiz []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' && s[i] != '\r' {
			temiz = append(temiz, s[i])
		}
	}
	out, err := base64Decode(string(temiz))
	if err != nil {
		panic(err)
	}
	return out
}
