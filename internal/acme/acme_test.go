package acme

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krmakmn/devbox/internal/certs"
)

func testStore(t *testing.T) *certs.Store {
	t.Helper()
	store, err := certs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(Config{
		Store:  testStore(t),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	srv.cfg.BaseURL = ts.URL + "/acme"
	return srv, ts
}

// istemci, testlerde kullanılan en küçük ACME istemcisi.
//
// Bu istemci protokolü benim anladığım gibi konuşuyor; gerçek
// doğrulama tests/acme-client altındaki bağımsız lego istemcisiyle
// yapılıyor. Buradaki testler sunucunun iç kurallarını (nonce tekrarı,
// url eşleşmesi, doğrulanmamış ad) sınıyor.
type istemci struct {
	t    *testing.T
	base string
	key  *ecdsa.PrivateKey
	kid  string
}

func yeniIstemci(t *testing.T, base string) *istemci {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &istemci{t: t, base: base, key: key}
}

func (c *istemci) nonce() string {
	resp, err := http.Head(c.base + "/new-nonce")
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Header.Get("Replay-Nonce")
}

func (c *istemci) jwk() map[string]string {
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(c.key.PublicKey.X.FillBytes(make([]byte, 32))),
		"y":   base64.RawURLEncoding.EncodeToString(c.key.PublicKey.Y.FillBytes(make([]byte, 32))),
	}
}

func (c *istemci) thumbprint() string {
	j := c.jwk()
	return thumbprint(fmt.Sprintf(`{"crv":%q,"kty":"EC","x":%q,"y":%q}`, j["crv"], j["x"], j["y"]))
}

// gonder, imzalı bir istek atar.
func (c *istemci) gonder(url string, payload any, nonce string) (*http.Response, []byte) {
	c.t.Helper()

	header := map[string]any{"alg": "ES256", "nonce": nonce, "url": url}
	if c.kid != "" {
		header["kid"] = c.kid
	} else {
		header["jwk"] = c.jwk()
	}
	headerJSON, _ := json.Marshal(header)
	protected := base64.RawURLEncoding.EncodeToString(headerJSON)

	var payloadB64 string
	if payload != nil {
		body, _ := json.Marshal(payload)
		payloadB64 = base64.RawURLEncoding.EncodeToString(body)
	}

	signed := protected + "." + payloadB64
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		c.t.Fatal(err)
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)

	env, _ := json.Marshal(jwsEnvelope{
		Protected: protected,
		Payload:   payloadB64,
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	})
	resp, err := http.Post(url, "application/jose+json", bytes.NewReader(env))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (c *istemci) hesapAc() {
	c.t.Helper()
	resp, _ := c.gonder(c.base+"/new-account",
		map[string]any{"termsOfServiceAgreed": true, "contact": []string{"mailto:a@b.test"}},
		c.nonce())
	if resp.StatusCode != http.StatusCreated {
		c.t.Fatalf("hesap açılamadı: %d", resp.StatusCode)
	}
	c.kid = resp.Header.Get("Location")
	if c.kid == "" {
		c.t.Fatal("hesap adresi (Location) verilmedi")
	}
}

func TestDirectoryListsRequiredEndpoints(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/acme/directory")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var dir map[string]any
	json.NewDecoder(resp.Body).Decode(&dir)
	for _, key := range []string{"newNonce", "newAccount", "newOrder", "revokeCert"} {
		if _, ok := dir[key]; !ok {
			t.Errorf("dizinde %q yok", key)
		}
	}
	// Her yanıt bir nonce taşımalı: istemci bir sonraki isteği bununla
	// imzalıyor.
	if resp.Header.Get("Replay-Nonce") == "" {
		t.Error("dizin yanıtında nonce yok")
	}
}

func TestAccountCreationIsIdempotent(t *testing.T) {
	_, ts := testServer(t)
	c := yeniIstemci(t, ts.URL+"/acme")
	c.hesapAc()
	ilkKID := c.kid

	// Aynı anahtarla ikinci kez: yeni hesap değil, var olan dönmeli.
	c.kid = ""
	resp, _ := c.gonder(c.base+"/new-account", map[string]any{"termsOfServiceAgreed": true}, c.nonce())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ikinci kayıt durumu = %d, 200 bekleniyordu", resp.StatusCode)
	}
	if resp.Header.Get("Location") != ilkKID {
		t.Errorf("aynı anahtara ikinci hesap açıldı: %q ≠ %q",
			resp.Header.Get("Location"), ilkKID)
	}
}

// Nonce tek kullanımlık: tekrar saldırısını kesen şey bu.
func TestNonceCannotBeReused(t *testing.T) {
	_, ts := testServer(t)
	c := yeniIstemci(t, ts.URL+"/acme")

	nonce := c.nonce()
	resp, _ := c.gonder(c.base+"/new-account", map[string]any{"termsOfServiceAgreed": true}, nonce)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ilk istek başarısız: %d", resp.StatusCode)
	}
	c.kid = ""
	resp, body := c.gonder(c.base+"/new-account", map[string]any{"termsOfServiceAgreed": true}, nonce)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("kullanılmış nonce kabul edildi: %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "badNonce") {
		t.Errorf("hata badNonce değil: %s", body)
	}
}

// İmzadaki url alanı isteğin gittiği yerle eşleşmeli; yoksa imzalı bir
// istek başka bir uç noktaya yönlendirilebilirdi.
func TestSignedURLMustMatchRequest(t *testing.T) {
	_, ts := testServer(t)
	c := yeniIstemci(t, ts.URL+"/acme")
	c.hesapAc()

	// url alanı new-order, istek new-account'a gidiyor.
	header := map[string]any{
		"alg": "ES256", "nonce": c.nonce(),
		"url": c.base + "/new-order", "kid": c.kid,
	}
	headerJSON, _ := json.Marshal(header)
	protected := base64.RawURLEncoding.EncodeToString(headerJSON)
	sum := sha256.Sum256([]byte(protected + "."))
	r, s, _ := ecdsa.Sign(rand.Reader, c.key, sum[:])
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	env, _ := json.Marshal(jwsEnvelope{
		Protected: protected,
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	})

	resp, err := http.Post(c.base+"/new-account", "application/jose+json", bytes.NewReader(env))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		t.Error("imzadaki url isteğe uymuyordu ama kabul edildi")
	}
}

func TestRejectsBadSignature(t *testing.T) {
	_, ts := testServer(t)
	c := yeniIstemci(t, ts.URL+"/acme")

	header := map[string]any{"alg": "ES256", "nonce": c.nonce(),
		"url": c.base + "/new-account", "jwk": c.jwk()}
	headerJSON, _ := json.Marshal(header)
	env, _ := json.Marshal(jwsEnvelope{
		Protected: base64.RawURLEncoding.EncodeToString(headerJSON),
		Payload:   base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		Signature: base64.RawURLEncoding.EncodeToString(make([]byte, 64)),
	})
	resp, err := http.Post(c.base+"/new-account", "application/jose+json", bytes.NewReader(env))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("geçersiz imza kabul edildi: %d", resp.StatusCode)
	}
}

func TestRejectsWrongContentType(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/acme/new-account", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("yanlış Content-Type kabul edildi: %d", resp.StatusCode)
	}
}

// Tam akış: sipariş, meydan okuma, doğrulama, sertifika.
func TestFullOrderFlow(t *testing.T) {
	srv, ts := testServer(t)

	// Meydan okumayı sunacak sahte sunucu.
	var beklenen string
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, beklenen)
	}))
	defer challenge.Close()
	srv.cfg.Resolve = func(string) (string, bool) {
		return strings.TrimPrefix(challenge.URL, "http://"), true
	}

	c := yeniIstemci(t, ts.URL+"/acme")
	c.hesapAc()

	resp, body := c.gonder(c.base+"/new-order", map[string]any{
		"identifiers": []identifier{{Type: "dns", Value: "api.magaza.test"}},
	}, c.nonce())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("sipariş oluşturulamadı: %d %s", resp.StatusCode, body)
	}
	orderURL := resp.Header.Get("Location")

	var ord struct {
		Status         string   `json:"status"`
		Authorizations []string `json:"authorizations"`
		Finalize       string   `json:"finalize"`
	}
	json.Unmarshal(body, &ord)
	if ord.Status != "pending" || len(ord.Authorizations) != 1 {
		t.Fatalf("sipariş = %+v", ord)
	}

	// Yetkilendirmeyi oku, meydan okumayı bul.
	_, body = c.gonder(ord.Authorizations[0], nil, c.nonce())
	var authz struct {
		Challenges []struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"challenges"`
	}
	json.Unmarshal(body, &authz)
	if len(authz.Challenges) != 1 || authz.Challenges[0].Type != "http-01" {
		t.Fatalf("yetkilendirme = %s", body)
	}

	beklenen = keyAuthorization(authz.Challenges[0].Token, c.thumbprint())

	// Meydan okumayı tetikle.
	_, body = c.gonder(authz.Challenges[0].URL, map[string]any{}, c.nonce())
	var chall struct {
		Status string `json:"status"`
	}
	json.Unmarshal(body, &chall)
	if chall.Status != "valid" {
		t.Fatalf("meydan okuma geçmedi: %s", body)
	}

	// CSR üret ve sonlandır.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "api.magaza.test"},
		DNSNames: []string{"api.magaza.test"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = c.gonder(ord.Finalize, map[string]any{
		"csr": base64.RawURLEncoding.EncodeToString(csrDER),
	}, c.nonce())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sonlandırma başarısız: %d %s", resp.StatusCode, body)
	}
	var son struct {
		Status      string `json:"status"`
		Certificate string `json:"certificate"`
	}
	json.Unmarshal(body, &son)
	if son.Status != "valid" || son.Certificate == "" {
		t.Fatalf("sipariş tamamlanmadı: %s", body)
	}

	// Sertifikayı indir ve kökümüze karşı doğrula.
	_, chain := c.gonder(son.Certificate, nil, c.nonce())
	block, rest := pem.Decode(chain)
	if block == nil {
		t.Fatalf("sertifika PEM değil: %s", chain)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "api.magaza.test" {
		t.Errorf("konu = %q", leaf.Subject.CommonName)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: srv.cfg.Store.RootPool()}); err != nil {
		t.Errorf("sertifika kökümüze zincirlenmiyor: %v", err)
	}
	// Zincirde kök de olmalı: istemciler onu sunucuya yükleyecek.
	if len(rest) == 0 {
		t.Error("zincirde ara/kök sertifika yok")
	}
	_ = orderURL
}

// Doğrulanmamış bir ad için sertifika verilmemeli: CSR'ın kendi SAN
// listesine güvenmek, herkesin istediği adı almasına yol açardı.
func TestCertificateOnlyCoversValidatedNames(t *testing.T) {
	srv, ts := testServer(t)

	var beklenen string
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, beklenen)
	}))
	defer challenge.Close()
	srv.cfg.Resolve = func(string) (string, bool) {
		return strings.TrimPrefix(challenge.URL, "http://"), true
	}

	c := yeniIstemci(t, ts.URL+"/acme")
	c.hesapAc()

	_, body := c.gonder(c.base+"/new-order", map[string]any{
		"identifiers": []identifier{{Type: "dns", Value: "kendi.magaza.test"}},
	}, c.nonce())
	var ord struct {
		Authorizations []string `json:"authorizations"`
		Finalize       string   `json:"finalize"`
	}
	json.Unmarshal(body, &ord)

	_, body = c.gonder(ord.Authorizations[0], nil, c.nonce())
	var authz struct {
		Challenges []struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"challenges"`
	}
	json.Unmarshal(body, &authz)
	beklenen = keyAuthorization(authz.Challenges[0].Token, c.thumbprint())
	c.gonder(authz.Challenges[0].URL, map[string]any{}, c.nonce())

	// CSR'da başka bir ad istiyoruz.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "banka.example.com"},
		DNSNames: []string{"banka.example.com", "kendi.magaza.test"},
	}, key)

	_, body = c.gonder(ord.Finalize, map[string]any{
		"csr": base64.RawURLEncoding.EncodeToString(csrDER),
	}, c.nonce())
	var son struct {
		Certificate string `json:"certificate"`
	}
	json.Unmarshal(body, &son)
	if son.Certificate == "" {
		t.Fatalf("sertifika verilmedi: %s", body)
	}

	_, chain := c.gonder(son.Certificate, nil, c.nonce())
	block, _ := pem.Decode(chain)
	leaf, _ := x509.ParseCertificate(block.Bytes)
	for _, name := range leaf.DNSNames {
		if name == "banka.example.com" {
			t.Fatal("doğrulanmamış ad sertifikaya girdi")
		}
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "kendi.magaza.test" {
		t.Errorf("SAN listesi = %v", leaf.DNSNames)
	}
}

// Meydan okuma yanlış cevap verirse sertifika verilmemeli.
func TestFailedChallengeBlocksIssuance(t *testing.T) {
	srv, ts := testServer(t)
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "yanlış cevap")
	}))
	defer challenge.Close()
	srv.cfg.Resolve = func(string) (string, bool) {
		return strings.TrimPrefix(challenge.URL, "http://"), true
	}

	c := yeniIstemci(t, ts.URL+"/acme")
	c.hesapAc()
	_, body := c.gonder(c.base+"/new-order", map[string]any{
		"identifiers": []identifier{{Type: "dns", Value: "magaza.test"}},
	}, c.nonce())
	var ord struct {
		Authorizations []string `json:"authorizations"`
		Finalize       string   `json:"finalize"`
	}
	json.Unmarshal(body, &ord)

	_, body = c.gonder(ord.Authorizations[0], nil, c.nonce())
	var authz struct {
		Challenges []struct{ URL string } `json:"challenges"`
	}
	json.Unmarshal(body, &authz)

	_, body = c.gonder(authz.Challenges[0].URL, map[string]any{}, c.nonce())
	if !strings.Contains(string(body), "invalid") {
		t.Errorf("yanlış cevap geçerli sayıldı: %s", body)
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "magaza.test"},
	}, key)
	resp, body := c.gonder(ord.Finalize, map[string]any{
		"csr": base64.RawURLEncoding.EncodeToString(csrDER),
	}, c.nonce())
	if resp.StatusCode == http.StatusOK {
		t.Errorf("doğrulama geçmeden sertifika verildi: %s", body)
	}
	if !strings.Contains(string(body), "orderNotReady") {
		t.Errorf("hata orderNotReady değil: %s", body)
	}
}

// JWK koordinatları eğrinin boyutunda sabit uzunlukta olmalı; kısa bir
// değeri kabul etmek aynı anahtarın farklı parmak izleriyle görünmesine
// yol açardı.
func TestJWKRejectsShortCoordinates(t *testing.T) {
	short := base64.RawURLEncoding.EncodeToString(big.NewInt(1).Bytes())
	raw := json.RawMessage(fmt.Sprintf(`{"kty":"EC","crv":"P-256","x":%q,"y":%q}`, short, short))
	if _, _, err := parseJWK(raw); err == nil {
		t.Error("kısa koordinat kabul edildi")
	}
}

// RFC 7638 parmak izi, bilinen bir vektörle sınanıyor.
func TestThumbprintMatchesRFC7638(t *testing.T) {
	// RFC 7638 §3.1'deki RSA örneği.
	n := "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw"
	raw := json.RawMessage(fmt.Sprintf(`{"kty":"RSA","n":%q,"e":"AQAB"}`, n))
	_, tp, err := parseJWK(raw)
	if err != nil {
		t.Fatal(err)
	}
	const want = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
	if tp != want {
		t.Errorf("parmak izi = %q, RFC 7638 örneği %q", tp, want)
	}
}
