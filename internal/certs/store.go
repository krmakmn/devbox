// Package certs, DevBox'ın yerel sertifika otoritesini yönetir.
//
// Amaç: geliştirici hiçbir şey yapmadan https://magaza.test açabilsin ve
// tarayıcı uyarı vermesin. Bunun için ilk açılışta bir kök CA üretiyor,
// siteler için ondan sertifika kesiyor ve süresi dolmadan sessizce
// yeniliyoruz.
//
// Tasarım kararları ve gerekçeleri:
//
//   - ECDSA P-256: RSA'ya göre çok daha hızlı üretilir (yüzlerce site için
//     fark ediyor), her güncel tarayıcı destekler.
//   - Kısa ömür (90 gün): yerel olarak güvenilen bir kök, sertifika şeffaflığı
//     ve 398 gün sınırından muaftır, yani teknik bir zorunluluk değil. Yine de
//     kısa tutuyoruz: yenileme yolu her gün kullanılmazsa bozulduğunda fark
//     edilmez.
//   - Joker SAN: magaza.test ile birlikte *.magaza.test de imzalanır, böylece
//     alt alan adları sıfır yapılandırmayla çalışır. Laragon'da bu yok.
//   - Kök anahtar diskte açık durur. Windows'ta DPAPI ile sarmalamak yol
//     haritasında; o zamana kadar dosya izinleri tek koruma.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// rootTTL, kök CA'nın ömrü. Uzun: kökü yenilemek her tarayıcıda güven
	// deposunu yeniden kurmak demek, geliştiricinin yılda bir yaşamak
	// isteyeceği bir şey değil.
	rootTTL = 10 * 365 * 24 * time.Hour

	// leafTTL, site sertifikalarının ömrü.
	leafTTL = 90 * 24 * time.Hour

	// renewBefore, bitişine bu kadar kalınca yenilenir.
	renewBefore = 30 * 24 * time.Hour

	// clockSkew, NotBefore'u geriye alma payı: makineler arası saat farkı
	// yüzünden "henüz geçerli değil" hatası almayalım.
	clockSkew = time.Hour
)

// Store, kök CA'yı ve kesilmiş site sertifikalarını tutar.
type Store struct {
	dir string

	// now, testlerde saati ileri almak için değiştirilebilir.
	now func() time.Time

	mu      sync.Mutex
	root    *x509.Certificate
	rootKey *ecdsa.PrivateKey
	rootPEM []byte
	cache   map[string]*tls.Certificate
}

// Open, verilen dizindeki sertifika deposunu açar; kök CA yoksa üretir.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("certs: dizin boş olamaz")
	}
	s := &Store{
		dir:   dir,
		now:   time.Now,
		cache: make(map[string]*tls.Certificate),
	}
	if err := os.MkdirAll(s.caDir(), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.sitesDir(), 0o700); err != nil {
		return nil, err
	}
	if err := s.loadOrCreateRoot(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) caDir() string        { return filepath.Join(s.dir, "ca") }
func (s *Store) sitesDir() string     { return filepath.Join(s.dir, "sites") }
func (s *Store) rootCertPath() string { return filepath.Join(s.caDir(), "root.crt") }
func (s *Store) rootKeyPath() string  { return filepath.Join(s.caDir(), "root.key") }

// RootPEM, kök sertifikanın PEM kodlaması. Güven deposuna kurulan şey budur.
func (s *Store) RootPEM() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.rootPEM...)
}

// RootCertPath, kök sertifika dosyasının yolu.
func (s *Store) RootCertPath() string { return s.rootCertPath() }

// RootCertificate, kök sertifikanın çözümlenmiş hâli.
func (s *Store) RootCertificate() *x509.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

// RootPool, kökü içeren bir doğrulama havuzu döner (testler ve istemciler için).
func (s *Store) RootPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.RootCertificate())
	return pool
}

func (s *Store) loadOrCreateRoot() error {
	certPEM, certErr := os.ReadFile(s.rootCertPath())
	keyPEM, keyErr := os.ReadFile(s.rootKeyPath())

	if certErr == nil && keyErr == nil {
		cert, key, err := parseKeyPair(certPEM, keyPEM)
		if err == nil && cert.NotAfter.After(s.now()) {
			s.root, s.rootKey, s.rootPEM = cert, key, certPEM
			return nil
		}
		// Bozuk ya da süresi dolmuş kök: yenisini üretmek, açıklaması zor bir
		// hatayla durmaktan iyi. Eskisi üzerine yazılır.
	}
	return s.createRoot()
}

func (s *Store) createRoot() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("certs: kök anahtar üretilemedi: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}

	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "DevBox yerel geliştirme CA",
			Organization: []string{"DevBox"},
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(rootTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// Ara CA üretmiyoruz; zincirin uzamasına izin vermemek saldırı
		// yüzeyini daraltır.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("certs: kök sertifika imzalanamadı: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}
	if err := writeFile(s.rootCertPath(), certPEM, 0o644); err != nil {
		return err
	}
	// Kök anahtar: yalnız sahibi okuyabilsin.
	if err := writeFile(s.rootKeyPath(), keyPEM, 0o600); err != nil {
		return err
	}

	s.root, s.rootKey, s.rootPEM = cert, key, certPEM
	return nil
}

// Certificate, verilen alan adı için sertifika döner; yoksa keser, süresi
// dolmak üzereyse yeniler.
//
// magaza.test için SAN listesi: magaza.test, *.magaza.test ve loopback
// adresleri. Joker sayesinde admin.magaza.test da ek iş yapmadan çalışır.
func (s *Store) Certificate(name string) (*tls.Certificate, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := validateName(name); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.cache[name]; ok && !s.needsRenewal(c) {
		return c, nil
	}
	if c, err := s.loadSite(name); err == nil && !s.needsRenewal(c) {
		s.cache[name] = c
		return c, nil
	}

	c, err := s.issue(name)
	if err != nil {
		return nil, err
	}
	s.cache[name] = c
	return c, nil
}

func (s *Store) needsRenewal(c *tls.Certificate) bool {
	if c == nil || c.Leaf == nil {
		return true
	}
	return s.now().Add(renewBefore).After(c.Leaf.NotAfter)
}

func (s *Store) issue(name string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   name,
			Organization: []string{"DevBox"},
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(leafTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsNamesFor(name),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.root, &key.PublicKey, s.rootKey)
	if err != nil {
		return nil, fmt.Errorf("certs: %s için sertifika imzalanamadı: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	if err := writeFile(s.siteCertPath(name), certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := writeFile(s.siteKeyPath(name), keyPEM, 0o600); err != nil {
		return nil, err
	}

	return &tls.Certificate{
		Certificate: [][]byte{der, s.root.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// dnsNamesFor, bir alan adı için SAN listesini üretir.
func dnsNamesFor(name string) []string {
	names := []string{name}
	// Joker yalnız bir seviye kapsar; zaten joker olan bir ada ikinci kez
	// joker eklemek anlamsız.
	if !strings.HasPrefix(name, "*.") {
		names = append(names, "*."+name)
	}
	if name != "localhost" {
		names = append(names, "localhost")
	}
	return names
}

func (s *Store) siteCertPath(name string) string {
	return filepath.Join(s.sitesDir(), safeFileName(name)+".crt")
}

func (s *Store) siteKeyPath(name string) string {
	return filepath.Join(s.sitesDir(), safeFileName(name)+".key")
}

func (s *Store) loadSite(name string) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(s.siteCertPath(name))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(s.siteKeyPath(name))
	if err != nil {
		return nil, err
	}
	leaf, key, err := parseKeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{leaf.Raw, s.root.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// GetCertificate, tls.Config'in sertifika geri çağrısıdır: SNI'de gelen ada
// göre sertifikayı üretir ya da önbellekten verir.
func (s *Store) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := hello.ServerName
	if name == "" {
		// SNI yok: IP ile bağlanılmış. localhost sertifikası ver.
		name = "localhost"
	}
	return s.Certificate(baseDomain(name))
}

// baseDomain, alt alan adını joker sertifikanın kapsadığı ana ada indirger:
// admin.magaza.test → magaza.test. Böylece her alt alan adı için ayrı
// sertifika kesmek gerekmez.
func baseDomain(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	labels := strings.Split(name, ".")
	if len(labels) <= 2 {
		return name
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// TLSConfig, doğrudan bir http.Server'a verilebilecek yapılandırma döner.
func (s *Store) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: s.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// --- yardımcılar ------------------------------------------------------------

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("certs: seri numarası üretilemedi: %w", err)
	}
	return serial, nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseKeyPair(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, nil, errors.New("certs: sertifika PEM'i çözülemedi")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("certs: anahtar PEM'i çözülemedi")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("certs: anahtar ECDSA değil")
	}
	return cert, key, nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	// Önce geçici dosyaya yazıp yerine koyuyoruz: yarım yazılmış bir
	// sertifika dosyası, sunucunun açılışta anlaşılmaz bir hatayla ölmesi
	// demek.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(path, perm)
}

// validateName, sertifika kesilecek adı denetler.
func validateName(name string) error {
	if name == "" {
		return errors.New("certs: alan adı boş")
	}
	if len(name) > 253 {
		return errors.New("certs: alan adı çok uzun")
	}
	if strings.ContainsAny(name, `/\:*?"<>| `) && !strings.HasPrefix(name, "*.") {
		return fmt.Errorf("certs: geçersiz alan adı %q", name)
	}
	for _, label := range strings.Split(strings.TrimPrefix(name, "*."), ".") {
		if label == "" {
			return fmt.Errorf("certs: geçersiz alan adı %q", name)
		}
	}
	return nil
}

// safeFileName, alan adını dosya adına çevirir. Joker yıldızı Windows'ta
// dosya adında kullanılamaz.
func safeFileName(name string) string {
	return strings.ReplaceAll(name, "*", "_wildcard_")
}

// SignCSR, bir sertifika isteğini (CSR) yerel CA ile imzalar.
//
// ACME sunucusu için var: orada anahtar çifti istemcide üretiliyor ve
// bize yalnız istek geliyor. Store'un kendi issue'sundan farkı bu —
// burada özel anahtarı görmüyoruz, dolayısıyla saklamıyoruz da.
//
// # CSR'daki hangi alanlara güveniliyor
//
// Yalnız açık anahtar ve imza. Konu adı ve SAN listesi çağırandan
// (ACME akışında doğrulanmış alan adlarından) geliyor — CSR'ın kendi
// SAN'ına güvenmek, doğrulanmamış bir ad için sertifika vermek demek
// olurdu.
func (s *Store) SignCSR(csr *x509.CertificateRequest, names []string) ([]byte, error) {
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("certs: CSR imzası geçersiz: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("certs: imzalanacak alan adı yok")
	}
	for _, name := range names {
		if err := validateName(name); err != nil {
			return nil, err
		}
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   names[0],
			Organization: []string{"DevBox"},
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(leafTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              names,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.root, csr.PublicKey, s.rootKey)
	if err != nil {
		return nil, fmt.Errorf("certs: CSR imzalanamadı: %w", err)
	}
	return der, nil
}

// Root, kök sertifikanın kendisi (zincire eklemek için).
func (s *Store) Root() *x509.Certificate { return s.root }

// Info, verilmiş bir sertifikanın özeti.
type Info struct {
	Name      string    `json:"name"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	DNSNames  []string  `json:"dnsNames"`
	Path      string    `json:"path"`

	// Expired ve NeedsRenewal, kullanıcıya durumu tek bakışta söyler.
	Expired      bool `json:"expired"`
	NeedsRenewal bool `json:"needsRenewal"`
}

// List, depodaki site sertifikalarını döner.
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.sitesDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := s.now()
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".crt") {
			continue
		}
		path := filepath.Join(s.sitesDir(), name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		block, _ := pem.Decode(data)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:         cert.Subject.CommonName,
			NotBefore:    cert.NotBefore,
			NotAfter:     cert.NotAfter,
			DNSNames:     cert.DNSNames,
			Path:         path,
			Expired:      now.After(cert.NotAfter),
			NeedsRenewal: now.Add(renewBefore).After(cert.NotAfter),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove, bir sitenin sertifikasını ve anahtarını siler.
//
// Bir sonraki istekte yenisi üretiliyor; komut "bozulmuş sertifikayı at,
// baştan üret" için var.
func (s *Store) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	certPath, keyPath := s.siteCertPath(name), s.siteKeyPath(name)
	if _, err := os.Stat(certPath); err != nil {
		return fmt.Errorf("certs: %s için sertifika yok", name)
	}
	s.mu.Lock()
	delete(s.cache, name)
	s.mu.Unlock()

	if err := os.Remove(certPath); err != nil {
		return err
	}
	return os.Remove(keyPath)
}
