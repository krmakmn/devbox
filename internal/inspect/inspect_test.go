package inspect

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func kaydedici(t *testing.T) *Recorder {
	t.Helper()
	r := NewRecorder(0, 0)
	r.SetEnabled(true)
	return r
}

// Kayıt kapalıyken hiçbir şey kopyalanmamalı: kapalı denetleyicinin
// maliyeti tek bir bayrak okuması olmalı.
func TestDisabledRecorderKeepsNothing(t *testing.T) {
	r := NewRecorder(0, 0)
	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "merhaba")
	})))
	defer srv.Close()

	http.Get(srv.URL + "/bir")
	if r.Count() != 0 {
		t.Errorf("kapalıyken %d kayıt tutuldu", r.Count())
	}
}

func TestRecordsRequestAndResponse(t *testing.T) {
	r := kaydedici(t)
	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		// İstek gövdesi aşağıdaki işleyiciye eksiksiz ulaşmalı.
		if string(body) != `{"ad":"Kerim"}` {
			t.Errorf("uygulamaya ulaşan gövde = %q", body)
		}
		w.Header().Set("X-Uygulama", "deneme")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"tamam":true}`)
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/siparis?kaynak=web", "application/json",
		strings.NewReader(`{"ad":"Kerim"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("%d kayıt", len(list))
	}
	ex, ok := r.Get(list[0].ID)
	if !ok {
		t.Fatal("kayıt kimliğiyle bulunamadı")
	}
	if ex.Method != "POST" || ex.Path != "/siparis" || ex.Query != "kaynak=web" {
		t.Errorf("istek = %+v", ex)
	}
	if ex.Status != http.StatusCreated {
		t.Errorf("durum = %d", ex.Status)
	}
	if ex.RequestBody != `{"ad":"Kerim"}` {
		t.Errorf("istek gövdesi = %q", ex.RequestBody)
	}
	if ex.ResponseBody != `{"tamam":true}` {
		t.Errorf("yanıt gövdesi = %q", ex.ResponseBody)
	}
	if got := ex.ResponseHeaders["X-Uygulama"]; len(got) != 1 || got[0] != "deneme" {
		t.Errorf("yanıt başlıkları = %v", ex.ResponseHeaders)
	}
	if ex.Duration == "" {
		t.Error("süre ölçülmemiş")
	}
}

// Yanıt istemciye eksiksiz gitmeli: kayıt araya girip veriyi
// yutmamalı.
func TestResponseReachesClientUntouched(t *testing.T) {
	r := kaydedici(t)
	buyuk := strings.Repeat("veri-", 50000) // gövde sınırının çok üstünde
	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, buyuk)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != buyuk {
		t.Errorf("istemciye giden gövde bozuldu: %d bayt, %d bekleniyordu", len(body), len(buyuk))
	}

	ex, _ := r.Get(r.List()[0].ID)
	if !ex.ResponseTruncated {
		t.Error("büyük gövde kesilmiş olarak işaretlenmemiş")
	}
	if len(ex.ResponseBody) > DefaultBodyLimit {
		t.Errorf("kaydedilen gövde sınırı aştı: %d", len(ex.ResponseBody))
	}
	if ex.ResponseSize != int64(len(buyuk)) {
		t.Errorf("boyut = %d, %d bekleniyordu", ex.ResponseSize, len(buyuk))
	}
}

func TestCapacityDropsOldest(t *testing.T) {
	r := NewRecorder(3, 0)
	r.SetEnabled(true)
	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})))
	defer srv.Close()

	for i := 0; i < 6; i++ {
		resp, err := http.Get(srv.URL + "/" + string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if r.Count() != 3 {
		t.Fatalf("%d kayıt tutuldu, 3 bekleniyordu", r.Count())
	}
	list := r.List()
	if list[0].Path != "/f" {
		t.Errorf("en yeni kayıt = %q", list[0].Path)
	}
}

func TestSubscribeAnnouncesNewExchanges(t *testing.T) {
	r := kaydedici(t)
	items, unsubscribe := r.Subscribe()
	defer unsubscribe()

	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/duyuru")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case item := <-items:
		if item.Path != "/duyuru" {
			t.Errorf("duyurulan kayıt = %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("yeni kayıt duyurulmadı")
	}
}

// Yavaş bir abone istekleri bekletmemeli.
func TestSlowSubscriberDoesNotBlockRequests(t *testing.T) {
	r := kaydedici(t)
	_, unsubscribe := r.Subscribe() // kimse okumuyor
	defer unsubscribe()

	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			resp, err := http.Get(srv.URL)
			if err != nil {
				return
			}
			resp.Body.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("yavaş abone istekleri bloke etti")
	}
}

func TestConcurrentRequests(t *testing.T) {
	r := kaydedici(t)
	srv := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "tamam")
	})))
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if r.Count() != 30 {
		t.Errorf("%d kayıt, 30 bekleniyordu", r.Count())
	}
	// Kimlikler benzersiz olmalı; yoksa arayüz yanlış kaydı gösterir.
	seen := make(map[string]bool)
	for _, s := range r.List() {
		if seen[s.ID] {
			t.Fatalf("kimlik tekrarlandı: %s", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestAPIListGetAndClear(t *testing.T) {
	r := kaydedici(t)
	upstream := httptest.NewServer(r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "gövde")
	})))
	defer upstream.Close()
	resp, _ := http.Get(upstream.URL + "/bir")
	resp.Body.Close()

	h := &Handler{Recorder: r, Domain: "magaza.test"}
	api := httptest.NewServer(h)
	defer api.Close()

	var list []Summary
	get(t, api.URL+"/api/exchanges", &list)
	if len(list) != 1 {
		t.Fatalf("%d kayıt listelendi", len(list))
	}

	var ex Exchange
	get(t, api.URL+"/api/exchanges/"+list[0].ID, &ex)
	if ex.ResponseBody != "gövde" {
		t.Errorf("kayıt gövdesi = %q", ex.ResponseBody)
	}

	req, _ := http.NewRequest(http.MethodDelete, api.URL+"/api/exchanges", nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if r.Count() != 0 {
		t.Error("temizleme çalışmadı")
	}
}

func TestAPICanToggleRecording(t *testing.T) {
	r := NewRecorder(0, 0)
	api := httptest.NewServer(&Handler{Recorder: r})
	defer api.Close()

	var state struct {
		Enabled bool `json:"enabled"`
	}
	get(t, api.URL+"/api/state", &state)
	if state.Enabled {
		t.Error("kaydedici kendiliğinden açık geldi")
	}

	resp, err := http.Post(api.URL+"/api/state", "application/json",
		strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&state)
	resp.Body.Close()
	if !state.Enabled || !r.Enabled() {
		t.Error("kayıt açılamadı")
	}
}

// Arayüz kaydedilen gövdeleri asla HTML olarak basmamalı: içerik
// incelenen uygulamadan geliyor.
func TestUIDoesNotRenderRecordedBodiesAsHTML(t *testing.T) {
	api := httptest.NewServer(&Handler{Recorder: NewRecorder(0, 0)})
	defer api.Close()

	resp, err := http.Get(api.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	if !strings.Contains(page, "kacar(metin)") {
		t.Error("gövde kaçırılmadan basılıyor olabilir")
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("içerik güvenliği ilkesi yok")
	}
	for _, forbidden := range []string{"http://", "https://cdn"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("arayüz dış kaynağa başvuruyor: %q", forbidden)
		}
	}
}

func get(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}
