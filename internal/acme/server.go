package acme

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/certs"
)

// DefaultAddr, ACME sunucusunun öntanımlı adresi.
//
// Yalnız geri döngü: imzasız bir CA'yı ağa açmak, o ağdaki herkese
// güvenilen sertifika dağıtmak demek olurdu.
const DefaultAddr = "127.0.0.1:14000"

// Config, sunucu ayarları.
type Config struct {
	// Store, sertifikaları imzalayacak yerel CA.
	Store *certs.Store

	// BaseURL, istemcilere gösterilecek kök adres
	// ("http://127.0.0.1:14000/acme"). Boşsa istekten türetilir.
	BaseURL string

	// Resolve, doğrulama isteğinin gideceği adresi belirler.
	//
	// Neden gerekiyor: http-01 doğrulaması alan adının 80. portuna
	// bağlanır. Bizim kenar vekilimiz zaten o portu tutuyor, yani
	// konteynerdeki istemcinin sunduğu meydan okumayı göremezdik. Bu
	// kanca, bir alan adı için "aslında şu adrese bak" demeyi sağlıyor.
	// Boşsa alan adının kendisine gidilir.
	Resolve func(domain string) (addr string, ok bool)

	Logger *slog.Logger
}

// Server, ACME sunucusu.
type Server struct {
	cfg Config

	mu       sync.Mutex
	nonces   map[string]time.Time
	accounts map[string]*account
	orders   map[string]*order
	authzs   map[string]*authorization
	challs   map[string]*challenge
	certsPEM map[string][]byte

	srv *http.Server
	ln  net.Listener
}

type account struct {
	ID         string
	Key        crypto.PublicKey
	Thumbprint string
	Contact    []string
	CreatedAt  time.Time
}

type order struct {
	ID          string
	AccountID   string
	Identifiers []identifier
	AuthzIDs    []string
	Status      string
	Expires     time.Time
	CertID      string
}

type authorization struct {
	ID         string
	OrderID    string
	Identifier identifier
	Status     string
	ChallID    string
	Expires    time.Time
}

type challenge struct {
	ID      string
	AuthzID string
	Token   string
	Status  string
	Error   string
}

type identifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// New, sunucuyu kurar.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("acme: sertifika deposu gerekli")
	}
	return &Server{
		cfg:      cfg,
		nonces:   make(map[string]time.Time),
		accounts: make(map[string]*account),
		orders:   make(map[string]*order),
		authzs:   make(map[string]*authorization),
		challs:   make(map[string]*challenge),
		certsPEM: make(map[string][]byte),
	}, nil
}

// Handler, ACME yollarını kurar.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /acme/directory", s.handleDirectory)
	mux.HandleFunc("HEAD /acme/new-nonce", s.handleNewNonce)
	mux.HandleFunc("GET /acme/new-nonce", s.handleNewNonce)
	mux.HandleFunc("POST /acme/new-account", s.handleNewAccount)
	mux.HandleFunc("POST /acme/new-order", s.handleNewOrder)
	mux.HandleFunc("POST /acme/order/{id}", s.handleOrder)
	mux.HandleFunc("POST /acme/order/{id}/finalize", s.handleFinalize)
	mux.HandleFunc("POST /acme/authz/{id}", s.handleAuthz)
	mux.HandleFunc("POST /acme/chall/{id}", s.handleChallenge)
	mux.HandleFunc("POST /acme/cert/{id}", s.handleCertificate)
	mux.HandleFunc("POST /acme/revoke-cert", s.handleRevoke)
	mux.HandleFunc("GET /acme/root.crt", s.handleRoot)
	return s.withNonce(mux)
}

// Start, sunucuyu dinlemeye başlatır.
func (s *Server) Start(addr string) (string, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("acme: dinlenemedi: %w", err)
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go s.srv.Serve(ln)
	return ln.Addr().String(), nil
}

// Close, sunucuyu kapatır.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// DirectoryURL, istemcilere verilecek adres.
func (s *Server) DirectoryURL() string {
	return s.base(nil) + "/directory"
}

// base, kök adresi döner.
func (s *Server) base(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimSuffix(s.cfg.BaseURL, "/")
	}
	host := "127.0.0.1"
	if s.ln != nil {
		host = s.ln.Addr().String()
	}
	if r != nil && r.Host != "" {
		host = r.Host
	}
	return "http://" + host + "/acme"
}

func (s *Server) logger() *slog.Logger {
	if s.cfg.Logger != nil {
		return s.cfg.Logger
	}
	return slog.Default()
}

// --- nonce ------------------------------------------------------------------

// withNonce, her yanıta yeni bir nonce ekler.
//
// ACME'de her imzalı istek bir kez kullanılabilen bir nonce taşıyor;
// tekrar saldırısını bu kesiyor. İstemci nonce'u önceki yanıttan alıyor,
// bu yüzden her yanıtta bir tane vermek zorundayız.
func (s *Server) withNonce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", s.newNonce())
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) newNonce() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	nonce := base64.RawURLEncoding.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Eski nonce'ları temizle: sınırsız birikmesinler.
	cutoff := time.Now().Add(-1 * time.Hour)
	for n, t := range s.nonces {
		if t.Before(cutoff) {
			delete(s.nonces, n)
		}
	}
	s.nonces[nonce] = time.Now()
	return nonce
}

// useNonce, nonce'u tüketir. İkinci kez kullanılamaz.
func (s *Server) useNonce(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nonces[nonce]; !ok {
		return false
	}
	delete(s.nonces, nonce)
	return true
}

// --- yanıt yardımcıları -----------------------------------------------------

// problem, RFC 7807 biçiminde hata yanıtı.
type problem struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func writeProblem(w http.ResponseWriter, status int, kind, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problem{
		Type:   "urn:ietf:params:acme:error:" + kind,
		Detail: detail,
		Status: status,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func newID() string {
	buf := make([]byte, 12)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// --- işleyiciler ------------------------------------------------------------

func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	base := s.base(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"newNonce":   base + "/new-nonce",
		"newAccount": base + "/new-account",
		"newOrder":   base + "/new-order",
		"revokeCert": base + "/revoke-cert",
		"meta": map[string]any{
			"website":                 "https://github.com/krmakmn/devbox",
			"termsOfService":          base + "/terms",
			"externalAccountRequired": false,
		},
	})
}

func (s *Server) handleNewNonce(w http.ResponseWriter, r *http.Request) {
	// Nonce başlığı ara katmanda zaten eklendi.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Write(s.cfg.Store.RootPEM())
}

// verify, imzalı isteği doğrular ve gövdesini döner.
func (s *Server) verify(w http.ResponseWriter, r *http.Request, requireAccount bool) (*jwsRequest, *account, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "application/jose+json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "malformed",
			fmt.Sprintf("Content-Type application/jose+json olmalı, %q geldi", ct))
		return nil, nil, false
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	defer body.Close()

	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}

	req, err := parseJWS(raw, s.lookupAccountKey)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed", err.Error())
		return nil, nil, false
	}
	if !s.useNonce(req.Header.Nonce) {
		writeProblem(w, http.StatusBadRequest, "badNonce", "nonce geçersiz ya da kullanılmış")
		return nil, nil, false
	}
	// url alanı isteğin gittiği adresle eşleşmeli: imzalı bir isteğin
	// başka bir uç noktaya yönlendirilmesini engelliyor.
	if want := s.base(r) + strings.TrimPrefix(r.URL.Path, "/acme"); req.Header.URL != want {
		writeProblem(w, http.StatusBadRequest, "unauthorized",
			fmt.Sprintf("imzadaki url %q, istek %q", req.Header.URL, want))
		return nil, nil, false
	}

	var acct *account
	if req.Header.KID != "" {
		s.mu.Lock()
		acct = s.accounts[accountIDFromKID(req.Header.KID)]
		s.mu.Unlock()
	}
	if requireAccount && acct == nil {
		writeProblem(w, http.StatusUnauthorized, "accountDoesNotExist", "hesap bulunamadı")
		return nil, nil, false
	}
	return req, acct, true
}

func accountIDFromKID(kid string) string {
	if i := strings.LastIndex(kid, "/"); i >= 0 {
		return kid[i+1:]
	}
	return kid
}

func (s *Server) lookupAccountKey(kid string) (crypto.PublicKey, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acct, ok := s.accounts[accountIDFromKID(kid)]
	if !ok {
		return nil, "", false
	}
	return acct.Key, acct.Thumbprint, true
}

func (s *Server) handleNewAccount(w http.ResponseWriter, r *http.Request) {
	req, _, ok := s.verify(w, r, false)
	if !ok {
		return
	}

	var payload struct {
		Contact              []string `json:"contact"`
		TermsOfServiceAgreed bool     `json:"termsOfServiceAgreed"`
		OnlyReturnExisting   bool     `json:"onlyReturnExisting"`
	}
	json.Unmarshal(req.Payload, &payload)

	s.mu.Lock()
	// Aynı anahtarla ikinci kez gelen istemciye var olan hesabı dön;
	// ACME bunu şart koşuyor.
	for _, acct := range s.accounts {
		if acct.Thumbprint == req.Thumbprint {
			id := acct.ID
			s.mu.Unlock()
			w.Header().Set("Location", s.base(r)+"/account/"+id)
			writeJSON(w, http.StatusOK, s.accountJSON(acct))
			return
		}
	}
	if payload.OnlyReturnExisting {
		s.mu.Unlock()
		writeProblem(w, http.StatusBadRequest, "accountDoesNotExist", "hesap yok")
		return
	}

	acct := &account{
		ID:         newID(),
		Key:        req.Key,
		Thumbprint: req.Thumbprint,
		Contact:    payload.Contact,
		CreatedAt:  time.Now(),
	}
	s.accounts[acct.ID] = acct
	s.mu.Unlock()

	s.logger().Info("ACME hesabı oluşturuldu", "hesap", acct.ID, "iletişim", acct.Contact)
	w.Header().Set("Location", s.base(r)+"/account/"+acct.ID)
	writeJSON(w, http.StatusCreated, s.accountJSON(acct))
}

func (s *Server) accountJSON(acct *account) map[string]any {
	return map[string]any{
		"status":  "valid",
		"contact": acct.Contact,
		"orders":  s.base(nil) + "/account/" + acct.ID + "/orders",
	}
}

func (s *Server) handleNewOrder(w http.ResponseWriter, r *http.Request) {
	req, acct, ok := s.verify(w, r, true)
	if !ok {
		return
	}

	var payload struct {
		Identifiers []identifier `json:"identifiers"`
	}
	if err := json.Unmarshal(req.Payload, &payload); err != nil || len(payload.Identifiers) == 0 {
		writeProblem(w, http.StatusBadRequest, "malformed", "identifiers alanı gerekli")
		return
	}
	for _, id := range payload.Identifiers {
		if id.Type != "dns" {
			writeProblem(w, http.StatusBadRequest, "unsupportedIdentifier",
				fmt.Sprintf("yalnız dns tanımlayıcısı destekleniyor, %q geldi", id.Type))
			return
		}
	}

	ord := &order{
		ID:          newID(),
		AccountID:   acct.ID,
		Identifiers: payload.Identifiers,
		Status:      "pending",
		Expires:     time.Now().Add(1 * time.Hour),
	}

	s.mu.Lock()
	for _, id := range payload.Identifiers {
		chall := &challenge{ID: newID(), Token: newID() + newID(), Status: "pending"}
		authz := &authorization{
			ID:         newID(),
			OrderID:    ord.ID,
			Identifier: id,
			Status:     "pending",
			ChallID:    chall.ID,
			Expires:    ord.Expires,
		}
		chall.AuthzID = authz.ID
		s.authzs[authz.ID] = authz
		s.challs[chall.ID] = chall
		ord.AuthzIDs = append(ord.AuthzIDs, authz.ID)
	}
	s.orders[ord.ID] = ord
	s.mu.Unlock()

	w.Header().Set("Location", s.base(r)+"/order/"+ord.ID)
	writeJSON(w, http.StatusCreated, s.orderJSON(r, ord))
}

func (s *Server) orderJSON(r *http.Request, ord *order) map[string]any {
	base := s.base(r)
	authzURLs := make([]string, 0, len(ord.AuthzIDs))
	for _, id := range ord.AuthzIDs {
		authzURLs = append(authzURLs, base+"/authz/"+id)
	}
	body := map[string]any{
		"status":         ord.Status,
		"expires":        ord.Expires.Format(time.RFC3339),
		"identifiers":    ord.Identifiers,
		"authorizations": authzURLs,
		"finalize":       base + "/order/" + ord.ID + "/finalize",
	}
	if ord.CertID != "" {
		body["certificate"] = base + "/cert/" + ord.CertID
	}
	return body
}

func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request) {
	_, acct, ok := s.verify(w, r, true)
	if !ok {
		return
	}
	s.mu.Lock()
	ord := s.orders[r.PathValue("id")]
	s.mu.Unlock()
	if ord == nil || ord.AccountID != acct.ID {
		writeProblem(w, http.StatusNotFound, "malformed", "sipariş bulunamadı")
		return
	}
	s.refreshOrder(ord)
	writeJSON(w, http.StatusOK, s.orderJSON(r, ord))
}

// refreshOrder, yetkilendirmelerin durumuna göre siparişi günceller.
func (s *Server) refreshOrder(ord *order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ord.Status != "pending" {
		return
	}
	for _, id := range ord.AuthzIDs {
		if s.authzs[id].Status != "valid" {
			return
		}
	}
	ord.Status = "ready"
}

func (s *Server) handleAuthz(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.verify(w, r, true); !ok {
		return
	}
	s.mu.Lock()
	authz := s.authzs[r.PathValue("id")]
	var chall *challenge
	if authz != nil {
		chall = s.challs[authz.ChallID]
	}
	s.mu.Unlock()
	if authz == nil {
		writeProblem(w, http.StatusNotFound, "malformed", "yetkilendirme bulunamadı")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     authz.Status,
		"expires":    authz.Expires.Format(time.RFC3339),
		"identifier": authz.Identifier,
		"challenges": []map[string]any{s.challengeJSON(r, chall)},
	})
}

func (s *Server) challengeJSON(r *http.Request, chall *challenge) map[string]any {
	body := map[string]any{
		"type":   "http-01",
		"url":    s.base(r) + "/chall/" + chall.ID,
		"token":  chall.Token,
		"status": chall.Status,
	}
	if chall.Error != "" {
		body["error"] = problem{
			Type:   "urn:ietf:params:acme:error:incorrectResponse",
			Detail: chall.Error,
			Status: http.StatusForbidden,
		}
	}
	return body
}

// handleChallenge, meydan okumayı tetikler ve doğrulamayı yapar.
//
// Doğrulama eşzamanlı: yerel bir ağda tek bir HTTP isteği milisaniyeler
// sürüyor ve istemcinin bekleme döngüsünü kısaltıyor.
func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	req, acct, ok := s.verify(w, r, true)
	if !ok {
		return
	}
	_ = req

	s.mu.Lock()
	chall := s.challs[r.PathValue("id")]
	var authz *authorization
	if chall != nil {
		authz = s.authzs[chall.AuthzID]
	}
	s.mu.Unlock()
	if chall == nil || authz == nil {
		writeProblem(w, http.StatusNotFound, "malformed", "meydan okuma bulunamadı")
		return
	}

	if chall.Status == "pending" {
		expected := keyAuthorization(chall.Token, acct.Thumbprint)
		err := s.validateHTTP01(authz.Identifier.Value, chall.Token, expected)

		s.mu.Lock()
		if err != nil {
			chall.Status = "invalid"
			chall.Error = err.Error()
			authz.Status = "invalid"
		} else {
			chall.Status = "valid"
			authz.Status = "valid"
		}
		s.mu.Unlock()

		if err != nil {
			s.logger().Warn("ACME doğrulaması başarısız",
				"alan", authz.Identifier.Value, "hata", err)
		} else {
			s.logger().Info("ACME doğrulaması geçti", "alan", authz.Identifier.Value)
		}
	}

	w.Header().Set("Link", fmt.Sprintf(`<%s/authz/%s>;rel="up"`, s.base(r), authz.ID))
	writeJSON(w, http.StatusOK, s.challengeJSON(r, chall))
}

func (s *Server) handleFinalize(w http.ResponseWriter, r *http.Request) {
	req, acct, ok := s.verify(w, r, true)
	if !ok {
		return
	}
	s.mu.Lock()
	ord := s.orders[r.PathValue("id")]
	s.mu.Unlock()
	if ord == nil || ord.AccountID != acct.ID {
		writeProblem(w, http.StatusNotFound, "malformed", "sipariş bulunamadı")
		return
	}
	s.refreshOrder(ord)
	if ord.Status != "ready" {
		writeProblem(w, http.StatusForbidden, "orderNotReady",
			fmt.Sprintf("sipariş durumu %q; önce meydan okumalar geçilmeli", ord.Status))
		return
	}

	var payload struct {
		CSR string `json:"csr"`
	}
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.CSR == "" {
		writeProblem(w, http.StatusBadRequest, "malformed", "csr alanı gerekli")
		return
	}
	der, err := base64.RawURLEncoding.DecodeString(payload.CSR)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed", "csr çözülemedi")
		return
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "badCSR", err.Error())
		return
	}

	// Sertifika yalnız doğrulanmış adlar için veriliyor; CSR'ın kendi
	// SAN listesine güvenmiyoruz.
	names := make([]string, 0, len(ord.Identifiers))
	for _, id := range ord.Identifiers {
		names = append(names, id.Value)
	}

	certDER, err := s.cfg.Store.SignCSR(csr, names)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "serverInternal", err.Error())
		return
	}

	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: s.cfg.Store.Root().Raw,
	})...)

	s.mu.Lock()
	certID := newID()
	s.certsPEM[certID] = chain
	ord.CertID = certID
	ord.Status = "valid"
	s.mu.Unlock()

	s.logger().Info("ACME sertifikası verildi", "alanlar", names)
	w.Header().Set("Location", s.base(r)+"/order/"+ord.ID)
	writeJSON(w, http.StatusOK, s.orderJSON(r, ord))
}

func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.verify(w, r, true); !ok {
		return
	}
	s.mu.Lock()
	chain := s.certsPEM[r.PathValue("id")]
	s.mu.Unlock()
	if chain == nil {
		writeProblem(w, http.StatusNotFound, "malformed", "sertifika bulunamadı")
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	w.Write(chain)
}

// handleRevoke, iptali kabul eder ama bir şey yapmaz.
//
// Yerel bir CA'da iptal listesi tutmanın karşılığı yok: sertifikalar 90
// gün ömürlü ve yalnız bu makinede geçerli. İstemcilerin akışı bozulmasın
// diye uç nokta yine de var; ne yaptığını açıkça söylüyor.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.verify(w, r, true); !ok {
		return
	}
	s.logger().Info("ACME iptal isteği alındı; yerel CA iptal listesi tutmuyor")
	w.WriteHeader(http.StatusOK)
}
