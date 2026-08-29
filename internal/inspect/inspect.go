// Package inspect, kenar vekilinden geçen istek ve yanıtları kaydeder.
//
// # Ne işe yarıyor
//
// "İstek gerçekten ne gönderdi, sunucu ne döndü?" sorusu geliştirmede
// günde onlarca kez soruluyor. Bugünkü cevaplar: tarayıcının geliştirici
// araçları (yalnız tarayıcıdan çıkan istekleri görür — sunucudan sunucuya
// gideni değil), ya da Charles/Proxyman gibi ücretli araçlar (ayrı bir
// vekil, ayrı bir kök sertifika, ayrı kurulum). DevBox'ın kenarı zaten
// bütün trafiğin geçtiği yer; kaydı oraya koymak ek bir bileşen
// gerektirmiyor.
//
// # Neden gövde sınırlı ve bellekte
//
// Kayıt bir hata ayıklama aracı, arşiv değil. Diske yazmak, kullanıcının
// haberi olmadan parola ve jeton içeren istekleri kalıcılaştırmak
// demekti. Bellekte, sayısı sınırlı ve DevBox kapanınca giden bir halka
// tamponu doğru dengeyi kuruyor. Gövdeler de sınırlı: bir dosya yükleme
// isteği belleği tek başına doldurabilir.
package inspect

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCapacity, saklanan en fazla değiş tokuş sayısı.
	DefaultCapacity = 200

	// DefaultBodyLimit, kaydedilecek en fazla gövde boyutu.
	//
	// 64 KB: bir HTML sayfası, JSON yanıtı ya da form gönderimi
	// neredeyse her zaman bunun altında. Dosya yüklemeleri kesiliyor ve
	// arayüz bunu söylüyor — kesilmiş gövde göstermek, belleği bir
	// yükleme isteğiyle doldurmaktan iyi.
	DefaultBodyLimit = 64 << 10
)

// Exchange, tek bir istek/yanıt çifti.
type Exchange struct {
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Duration string    `json:"duration"`

	Host   string `json:"host"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query,omitempty"`
	Proto  string `json:"proto"`

	RequestHeaders   map[string][]string `json:"requestHeaders"`
	RequestBody      string              `json:"requestBody,omitempty"`
	RequestTruncated bool                `json:"requestTruncated,omitempty"`

	Status            int                 `json:"status"`
	ResponseHeaders   map[string][]string `json:"responseHeaders"`
	ResponseBody      string              `json:"responseBody,omitempty"`
	ResponseTruncated bool                `json:"responseTruncated,omitempty"`
	ResponseSize      int64               `json:"responseSize"`

	// Error, isteğin karşılanamadığı durumlarda sebebi.
	Error string `json:"error,omitempty"`
}

// Summary, listede gösterilecek özet.
type Summary struct {
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Duration string    `json:"duration"`
	Host     string    `json:"host"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Size     int64     `json:"size"`
}

// Summary, değiş tokuşun özetini döner.
func (e *Exchange) Summary() Summary {
	return Summary{
		ID: e.ID, Started: e.Started, Duration: e.Duration,
		Host: e.Host, Method: e.Method, Path: e.Path,
		Status: e.Status, Size: e.ResponseSize,
	}
}

// Recorder, değiş tokuşları tutar ve abonelere duyurur.
type Recorder struct {
	capacity  int
	bodyLimit int

	mu        sync.RWMutex
	exchanges []*Exchange
	byID      map[string]*Exchange
	enabled   bool
	seq       uint64

	subs   map[int]chan Summary
	nextID int
}

// NewRecorder, kaydediciyi kurar.
//
// Kapalı başlıyor: her isteğin gövdesini bellekte tutmak, kullanıcının
// istemediği bir maliyet. Denetleyici açıldığında kayıt başlıyor.
func NewRecorder(capacity, bodyLimit int) *Recorder {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if bodyLimit <= 0 {
		bodyLimit = DefaultBodyLimit
	}
	return &Recorder{
		capacity:  capacity,
		bodyLimit: bodyLimit,
		byID:      make(map[string]*Exchange),
		subs:      make(map[int]chan Summary),
	}
}

// SetEnabled, kaydı açar ya da kapatır.
func (r *Recorder) SetEnabled(on bool) {
	r.mu.Lock()
	r.enabled = on
	r.mu.Unlock()
}

// Enabled, kaydın açık olup olmadığını söyler.
func (r *Recorder) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// List, değiş tokuşları en yeniden eskiye döner.
func (r *Recorder) List() []Summary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Summary, 0, len(r.exchanges))
	for i := len(r.exchanges) - 1; i >= 0; i-- {
		out = append(out, r.exchanges[i].Summary())
	}
	return out
}

// Get, kimliğe göre değiş tokuşu döner.
func (r *Recorder) Get(id string) (*Exchange, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ex, ok := r.byID[id]
	return ex, ok
}

// Clear, kaydı temizler.
func (r *Recorder) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exchanges = nil
	r.byID = make(map[string]*Exchange)
}

// Count, kayıtlı değiş tokuş sayısı.
func (r *Recorder) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.exchanges)
}

// Subscribe, yeni kayıtları alacak bir kanal döner.
func (r *Recorder) Subscribe() (<-chan Summary, func()) {
	ch := make(chan Summary, 32)
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.subs[id] = ch
	r.mu.Unlock()

	return ch, func() {
		r.mu.Lock()
		if existing, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(existing)
		}
		r.mu.Unlock()
	}
}

func (r *Recorder) add(ex *Exchange) {
	r.mu.Lock()
	r.exchanges = append(r.exchanges, ex)
	r.byID[ex.ID] = ex
	for len(r.exchanges) > r.capacity {
		oldest := r.exchanges[0]
		r.exchanges = r.exchanges[1:]
		delete(r.byID, oldest.ID)
	}
	subs := make([]chan Summary, 0, len(r.subs))
	for _, ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()

	summary := ex.Summary()
	for _, ch := range subs {
		// Yavaş abone isteği bekletmemeli.
		select {
		case ch <- summary:
		default:
		}
	}
}

func (r *Recorder) nextExchangeID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return strconv.FormatUint(r.seq, 36) + "-" + strconv.FormatInt(time.Now().UnixNano()%1e6, 36)
}

// Middleware, bir işleyiciyi kayıt altına alır.
//
// Kayıt kapalıyken hiçbir şey kopyalanmıyor: kapalı denetleyicinin
// maliyeti tek bir bayrak okuması.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.Enabled() {
			next.ServeHTTP(w, req)
			return
		}

		start := time.Now()
		ex := &Exchange{
			ID:             r.nextExchangeID(),
			Started:        start,
			Host:           req.Host,
			Method:         req.Method,
			Path:           req.URL.Path,
			Query:          req.URL.RawQuery,
			Proto:          req.Proto,
			RequestHeaders: cloneHeader(req.Header),
		}

		// İstek gövdesi okunduktan sonra aşağıdaki işleyiciye yeniden
		// verilmeli; yoksa uygulama boş gövde görür.
		if req.Body != nil {
			body, truncated := readLimited(req.Body, r.bodyLimit)
			req.Body.Close()
			ex.RequestBody = string(body)
			ex.RequestTruncated = truncated
			req.Body = io.NopCloser(bytes.NewReader(body))
		}

		rec := &responseRecorder{
			ResponseWriter: w,
			limit:          r.bodyLimit,
			status:         http.StatusOK,
		}
		next.ServeHTTP(rec, req)

		ex.Duration = time.Since(start).Round(time.Microsecond).String()
		ex.Status = rec.status
		ex.ResponseHeaders = cloneHeader(w.Header())
		ex.ResponseBody = rec.body.String()
		ex.ResponseTruncated = rec.truncated
		ex.ResponseSize = rec.size
		r.add(ex)
	})
}

// responseRecorder, yanıtı hem istemciye geçirir hem kopyalar.
type responseRecorder struct {
	http.ResponseWriter
	limit     int
	status    int
	size      int64
	body      bytes.Buffer
	truncated bool
	wrote     bool
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	w.size += int64(len(p))
	if remaining := w.limit - w.body.Len(); remaining > 0 {
		if len(p) <= remaining {
			w.body.Write(p)
		} else {
			w.body.Write(p[:remaining])
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return w.ResponseWriter.Write(p)
}

// Flush, akış yanıtlarının (SSE) çalışmaya devam etmesi için gerekli.
func (w *responseRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func readLimited(r io.Reader, limit int) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return body, false
	}
	if len(body) > limit {
		return body[:limit], true
	}
	return body, false
}

func cloneHeader(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		// Yetkilendirme başlıkları kayda giriyor: bu bir hata ayıklama
		// aracı ve "neden 401 aldım" sorusunun cevabı çoğu zaman tam
		// olarak orada. Kayıt yalnız bellekte ve yalnız geri döngüden
		// erişilebilir olduğu için bedeli kabul ediyoruz.
		out[k] = append([]string(nil), v...)
	}
	return out
}

// ContentType, değiş tokuşun yanıt türünü döner.
func (e *Exchange) ContentType() string {
	for k, v := range e.ResponseHeaders {
		if strings.EqualFold(k, "Content-Type") && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}
