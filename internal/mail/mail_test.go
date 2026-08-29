package mail

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newServer, rastgele bir portta yakalayıcı başlatır.
func newServer(t *testing.T) *SMTPServer {
	t.Helper()
	srv := &SMTPServer{Addr: "127.0.0.1:0", Store: NewStore(0)}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// send, standart kütüphanenin SMTP istemcisiyle posta gönderir.
//
// Kendi istemcimizi yazmak yerine stdlib'i kullanıyoruz: sunucumuz bizden
// bağımsız yazılmış gerçek bir istemciyle konuşmuş oluyor. Aynı yanlış
// anlamayı iki yerde birden yapma riski kalkıyor.
func send(t *testing.T, srv *SMTPServer, from string, to []string, body string) {
	t.Helper()
	if err := smtp.SendMail(srv.ListenAddr(), nil, from, to, []byte(body)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}
}

func waitFor(t *testing.T, srv *SMTPServer, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Store.Count() >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d posta bekleniyordu, %d geldi", count, srv.Store.Count())
}

func TestCatchesSimpleMessage(t *testing.T) {
	srv := newServer(t)

	send(t, srv, "gonderen@magaza.test", []string{"alici@ornek.test"},
		"From: gonderen@magaza.test\r\n"+
			"To: alici@ornek.test\r\n"+
			"Subject: Siparişiniz hazır\r\n"+
			"\r\n"+
			"Merhaba, siparişiniz kargoya verildi.\r\n")

	waitFor(t, srv, 1)
	msg, ok := srv.Store.Latest()
	if !ok {
		t.Fatal("posta depoda yok")
	}

	if msg.From != "gonderen@magaza.test" {
		t.Errorf("gönderen = %q", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0] != "alici@ornek.test" {
		t.Errorf("alıcı = %v", msg.To)
	}
	if msg.Subject != "Siparişiniz hazır" {
		t.Errorf("konu = %q", msg.Subject)
	}
	if !strings.Contains(msg.Text, "kargoya verildi") {
		t.Errorf("gövde = %q", msg.Text)
	}
	if msg.ID == "" {
		t.Error("kimlik atanmadı")
	}
	if msg.Size == 0 || len(msg.Raw) == 0 {
		t.Error("ham hâl saklanmadı")
	}
}

// Laravel, Symfony ve WordPress eklentileri çoğunlukla kullanıcı adı/parola
// ile yapılandırılmış geliyor; sunucu AUTH desteklemezse bağlantıyı hata
// sayıyorlar.
func TestAcceptsAnyCredentials(t *testing.T) {
	srv := newServer(t)
	host, _, _ := strings.Cut(srv.ListenAddr(), ":")

	auth := smtp.PlainAuth("", "herhangi", "parola", host)
	err := smtp.SendMail(srv.ListenAddr(), auth,
		"a@magaza.test", []string{"b@ornek.test"},
		[]byte("Subject: kimlikli\r\n\r\ngövde\r\n"))
	if err != nil {
		t.Fatalf("kimlik doğrulamalı gönderim başarısız: %v", err)
	}
	waitFor(t, srv, 1)
}

// Türkçe konu satırları neredeyse her zaman RFC 2047 ile kodlanmış geliyor;
// çözmezsek arayüzde okunmaz bir dizi görünür.
func TestDecodesEncodedSubject(t *testing.T) {
	srv := newServer(t)

	// "Şifre sıfırlama" — UTF-8 base64.
	send(t, srv, "a@x.test", []string{"b@y.test"},
		"Subject: =?UTF-8?B?xZ5pZnJlIHPEsWbEsXJsYW1h?=\r\n"+
			"\r\n"+
			"gövde\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if msg.Subject != "Şifre sıfırlama" {
		t.Errorf("konu = %q, beklenen %q", msg.Subject, "Şifre sıfırlama")
	}
}

func TestParsesMultipartWithAttachment(t *testing.T) {
	srv := newServer(t)

	body := "Subject: Fatura\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=SINIR\r\n" +
		"\r\n" +
		"--SINIR\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		"Faturanız ektedir.\r\n" +
		"--SINIR\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n" +
		"\r\n" +
		"<p>Faturanız <b>ektedir</b>.</p>\r\n" +
		"--SINIR\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"fatura.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n" +
		"--SINIR--\r\n"

	send(t, srv, "a@x.test", []string{"b@y.test"}, body)
	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()

	if !strings.Contains(msg.Text, "Faturanız ektedir") {
		t.Errorf("düz metin parçası = %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "<b>ektedir</b>") {
		t.Errorf("HTML parçası = %q", msg.HTML)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("%d ek bulundu, beklenen 1: %+v", len(msg.Attachments), msg.Attachments)
	}
	att := msg.Attachments[0]
	if att.Filename != "fatura.pdf" {
		t.Errorf("ek adı = %q", att.Filename)
	}
	if att.ContentType != "application/pdf" {
		t.Errorf("ek türü = %q", att.ContentType)
	}
	// base64 çözülmüş olmalı: "%PDF-1.4"
	if !strings.HasPrefix(string(att.Data), "%PDF") {
		t.Errorf("ek içeriği çözülmemiş: %q", att.Data)
	}
}

func TestDecodesQuotedPrintable(t *testing.T) {
	srv := newServer(t)
	send(t, srv, "a@x.test", []string{"b@y.test"},
		"Subject: qp\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: quoted-printable\r\n"+
			"\r\n"+
			"Merhaba =C3=87i=C3=A7ek\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if !strings.Contains(msg.Text, "Çiçek") {
		t.Errorf("quoted-printable çözülmedi: %q", msg.Text)
	}
}

// Satır başındaki tek nokta, protokolün sonlandırıcısıyla karışmasın diye
// istemci tarafından ikilenir (RFC 5321 §4.5.2); sunucu bunu geri almalı.
func TestUndoesDotStuffing(t *testing.T) {
	srv := newServer(t)
	send(t, srv, "a@x.test", []string{"b@y.test"},
		"Subject: nokta\r\n\r\nbirinci satır\r\n.gizli satır\r\nson satır\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if !strings.Contains(msg.Text, ".gizli satır") {
		t.Errorf("nokta doldurma geri alınmadı: %q", msg.Text)
	}
	if strings.Contains(msg.Text, "..gizli") {
		t.Errorf("nokta ikilenmiş kaldı: %q", msg.Text)
	}
}

// Zarf başlıklardan farklı olabilir; postanın gerçekten nereye gittiğini
// zarf söyler.
func TestEnvelopeWinsOverHeaders(t *testing.T) {
	srv := newServer(t)
	send(t, srv, "zarf@gercek.test", []string{"zarf-alici@gercek.test"},
		"From: baslik@yalan.test\r\nTo: baslik-alici@yalan.test\r\n\r\ngövde\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if msg.From != "zarf@gercek.test" {
		t.Errorf("gönderen = %q, zarf adresi beklenirdi", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0] != "zarf-alici@gercek.test" {
		t.Errorf("alıcı = %v, zarf adresi beklenirdi", msg.To)
	}
}

// Bozuk bir posta da yakalanmalı ve arayüzde görünmeli.
func TestKeepsUnparseableMessage(t *testing.T) {
	srv := newServer(t)
	send(t, srv, "a@x.test", []string{"b@y.test"}, "bu geçerli bir posta değil\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if msg.Text == "" && len(msg.Raw) == 0 {
		t.Error("çözümlenemeyen posta tümüyle kayboldu")
	}
}

func TestMultipleRecipients(t *testing.T) {
	srv := newServer(t)
	send(t, srv, "a@x.test", []string{"b@y.test", "c@z.test", "d@w.test"},
		"Subject: çoklu\r\n\r\ngövde\r\n")

	waitFor(t, srv, 1)
	msg, _ := srv.Store.Latest()
	if len(msg.To) != 3 {
		t.Errorf("alıcı sayısı %d, beklenen 3: %v", len(msg.To), msg.To)
	}
}

// --- depo -----------------------------------------------------------------

func TestStoreCapacityDropsOldest(t *testing.T) {
	s := NewStore(3)
	for i := 0; i < 5; i++ {
		s.Add(&Message{ID: fmt.Sprintf("m%d", i), Subject: fmt.Sprintf("konu %d", i)})
	}

	if s.Count() != 3 {
		t.Fatalf("%d posta saklandı, kapasite 3", s.Count())
	}
	if _, ok := s.Get("m0"); ok {
		t.Error("en eski posta düşürülmedi")
	}
	if _, ok := s.Get("m4"); !ok {
		t.Error("en yeni posta yok")
	}

	// Liste en yeniden eskiye sıralı olmalı.
	list := s.List()
	if len(list) != 3 || list[0].ID != "m4" || list[2].ID != "m2" {
		t.Errorf("liste sırası yanlış: %+v", list)
	}
}

func TestStoreSubscribe(t *testing.T) {
	s := NewStore(0)
	ch, unsubscribe := s.Subscribe()

	go s.Add(&Message{ID: "x", Subject: "canlı"})

	select {
	case msg := <-ch:
		if msg.Subject != "canlı" {
			t.Errorf("gelen posta = %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("abone bildirim almadı")
	}

	unsubscribe()
	if _, ok := <-ch; ok {
		t.Error("abonelik sonlandıktan sonra kanal kapanmadı")
	}
}

// Yavaş bir abone yüzünden SMTP oturumu beklememeli.
func TestSlowSubscriberDoesNotBlockDelivery(t *testing.T) {
	s := NewStore(0)
	_, unsubscribe := s.Subscribe() // hiç okumayan abone
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			s.Add(&Message{ID: fmt.Sprintf("m%d", i)})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("yavaş abone teslimatı kilitledi")
	}
}

func TestStoreDeleteAndClear(t *testing.T) {
	s := NewStore(0)
	s.Add(&Message{ID: "a"})
	s.Add(&Message{ID: "b"})

	if !s.Delete("a") {
		t.Error("var olan posta silinemedi")
	}
	if s.Delete("yok") {
		t.Error("olmayan posta silindi denildi")
	}
	if s.Count() != 1 {
		t.Errorf("silmeden sonra %d posta", s.Count())
	}

	s.Clear()
	if s.Count() != 0 {
		t.Error("temizlemeden sonra posta kaldı")
	}
}

func TestConcurrentDeliveries(t *testing.T) {
	srv := newServer(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := smtp.SendMail(srv.ListenAddr(), nil,
				fmt.Sprintf("a%d@x.test", i), []string{"b@y.test"},
				[]byte(fmt.Sprintf("Subject: posta %d\r\n\r\ngövde\r\n", i)))
			if err != nil {
				t.Errorf("gönderim %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	waitFor(t, srv, 20)
}

// --- HTTP arayüzü ---------------------------------------------------------

func newAPI(t *testing.T) (*httptest.Server, *SMTPServer) {
	t.Helper()
	smtpSrv := newServer(t)
	api := httptest.NewServer(&Handler{Store: smtpSrv.Store, SMTPAddr: smtpSrv.ListenAddr()})
	t.Cleanup(api.Close)
	return api, smtpSrv
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("JSON çözülemedi: %v", err)
		}
	}
	return resp.StatusCode
}

// Kabul kriteri: gönderilen postanın gövdesi API'den okunabilmeli.
func TestAPIReturnsLatestMessage(t *testing.T) {
	api, srv := newAPI(t)

	send(t, srv, "a@magaza.test", []string{"b@ornek.test"},
		"Subject: Şifre sıfırlama\r\n\r\nBağlantı: https://magaza.test/sifirla/abc\r\n")
	waitFor(t, srv, 1)

	var msg Message
	if code := getJSON(t, api.URL+"/api/latest", &msg); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if msg.Subject != "Şifre sıfırlama" {
		t.Errorf("konu = %q", msg.Subject)
	}
	if !strings.Contains(msg.Text, "sifirla/abc") {
		t.Errorf("gövde = %q", msg.Text)
	}
}

func TestAPIListAndGet(t *testing.T) {
	api, srv := newAPI(t)
	send(t, srv, "a@x.test", []string{"b@y.test"}, "Subject: bir\r\n\r\ngövde\r\n")
	send(t, srv, "a@x.test", []string{"b@y.test"}, "Subject: iki\r\n\r\ngövde\r\n")
	waitFor(t, srv, 2)

	var list []Summary
	getJSON(t, api.URL+"/api/messages", &list)
	if len(list) != 2 {
		t.Fatalf("%d özet döndü", len(list))
	}
	// En yeni başta.
	if list[0].Subject != "iki" {
		t.Errorf("sıra yanlış: %q", list[0].Subject)
	}

	var msg Message
	if code := getJSON(t, api.URL+"/api/messages/"+list[0].ID, &msg); code != http.StatusOK {
		t.Fatalf("durum %d", code)
	}
	if msg.Subject != "iki" {
		t.Errorf("konu = %q", msg.Subject)
	}

	if code := getJSON(t, api.URL+"/api/messages/olmayan", nil); code != http.StatusNotFound {
		t.Errorf("olmayan posta için durum %d", code)
	}
}

// Yakalanan bir HTML ya da SVG eki tarayıcıda açılırsa, bu köken üzerinde
// betik çalıştırabilir.
func TestAttachmentsAreDownloadedNotRendered(t *testing.T) {
	api, srv := newAPI(t)

	body := "Subject: ek\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=S\r\n\r\n" +
		"--S\r\nContent-Type: text/plain\r\n\r\nmetin\r\n" +
		"--S\r\nContent-Type: text/html\r\n" +
		"Content-Disposition: attachment; filename=\"kotu.html\"\r\n\r\n" +
		"<script>alert(1)</script>\r\n--S--\r\n"
	send(t, srv, "a@x.test", []string{"b@y.test"}, body)
	waitFor(t, srv, 1)

	msg, _ := srv.Store.Latest()
	resp, err := http.Get(api.URL + "/api/messages/" + msg.ID + "/attachments/0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("ek Content-Type = %q; tarayıcıda açılabilir", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("ek indirilmek yerine görüntüleniyor: %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("içerik türü tahmini engellenmemiş")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"fatura.pdf":      "fatura.pdf",
		"../../gecti.pdf": ".._.._gecti.pdf",
		`a"b.pdf`:         "a_b.pdf",
		"a\r\nb.pdf":      "ab.pdf",
		"":                "ek",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func TestAPIDeleteAndClear(t *testing.T) {
	api, srv := newAPI(t)
	send(t, srv, "a@x.test", []string{"b@y.test"}, "Subject: bir\r\n\r\ng\r\n")
	send(t, srv, "a@x.test", []string{"b@y.test"}, "Subject: iki\r\n\r\ng\r\n")
	waitFor(t, srv, 2)

	msg, _ := srv.Store.Latest()
	req, _ := http.NewRequest(http.MethodDelete, api.URL+"/api/messages/"+msg.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if srv.Store.Count() != 1 {
		t.Errorf("silmeden sonra %d posta", srv.Store.Count())
	}

	req, _ = http.NewRequest(http.MethodDelete, api.URL+"/api/messages", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if srv.Store.Count() != 0 {
		t.Errorf("temizlemeden sonra %d posta", srv.Store.Count())
	}
}

// Kabul kriteri: posta 200 ms içinde arayüze ulaşmalı.
func TestAPIStreamDeliversQuickly(t *testing.T) {
	api, srv := newAPI(t)

	resp, err := http.Get(api.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				lines <- data
			}
		}
	}()

	// Akışın kurulmasına kısa bir an tanı.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	send(t, srv, "a@x.test", []string{"b@y.test"}, "Subject: canlı\r\n\r\ngövde\r\n")

	select {
	case data := <-lines:
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("posta akışa %v sonra düştü, 200 ms bekleniyordu", elapsed)
		}
		if !strings.Contains(data, "canlı") {
			t.Errorf("akıştan gelen = %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("posta canlı akışta görünmedi")
	}
}

func TestIndexPageIsSelfContained(t *testing.T) {
	api, _ := newAPI(t)
	resp, err := http.Get(api.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	// DevBox çevrimdışı da çalışmalı: dış kaynak olmamalı.
	for _, forbidden := range []string{"http://", "https://cdn", "//cdn."} {
		if strings.Contains(page, forbidden) {
			t.Errorf("arayüz dış kaynağa başvuruyor: %q", forbidden)
		}
	}
	// HTML gövde yalıtılmış çerçevede gösterilmeli.
	if !strings.Contains(page, "sandbox") {
		t.Error("HTML gövde için sandbox kullanılmıyor")
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("içerik güvenliği ilkesi yok")
	}
}

// HTML gövde, üst sayfanın ilkesini devralan bir srcdoc çerçevesinde değil,
// kendi katı ilkesini taşıyan ayrı bir belgede sunulmalı: üst sayfanın
// ilkesinde satır içi betik açık olduğundan, devralınan ilke posta HTML'ine
// de betik izni verirdi.
func TestHTMLBodyServedWithOwnStrictPolicy(t *testing.T) {
	api, srv := newAPI(t)

	body := "Subject: html\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<h1>merhaba</h1><script>alert(1)</script>" +
		"<img src=\"http://takip.example/piksel.gif\">\r\n"
	send(t, srv, "a@x.test", []string{"b@y.test"}, body)
	waitFor(t, srv, 1)

	msg, _ := srv.Store.Latest()
	resp, err := http.Get(api.URL + "/api/messages/" + msg.ID + "/html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "<h1>merhaba</h1>") {
		t.Errorf("HTML gövde olduğu gibi sunulmamış: %q", got)
	}

	csp := resp.Header.Get("Content-Security-Policy")
	// sandbox: adres çubuğundan doğrudan açılsa bile adressiz köken.
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("gövde ilkesinde sandbox yok: %q", csp)
	}
	// default-src 'none': betik yok, uzak takip pikseli yok.
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("gövde ilkesi dış kaynağı engellemiyor: %q", csp)
	}
	if strings.Contains(csp, "script-src") {
		t.Errorf("gövde ilkesi betiğe izin veriyor gibi: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("içerik türü tahmini engellenmemiş")
	}
}

// Arayüz çerçeveyi kendi kökeninden yüklüyor; frame-src 'none' onu engellerdi.
func TestIndexPolicyAllowsOwnFrame(t *testing.T) {
	api, _ := newAPI(t)
	resp, err := http.Get(api.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src 'self'") {
		t.Errorf("HTML gövde çerçevesi kendi ilkemizce engelleniyor: %q", csp)
	}
	// Gömülü simge data: adresinde; img-src buna izin vermeli.
	if !strings.Contains(csp, "img-src 'self' data:") {
		t.Errorf("gömülü simge ilkece engelleniyor: %q", csp)
	}
	if !strings.Contains(string(body), `rel="icon"`) {
		t.Error("gömülü simge yok: tarayıcı /favicon.ico isteyip 404 alıyor")
	}
}

// Arama, geliştiricinin gerçekten aradığı yerlere bakmalı: konu kadar
// gövde, adres ve ek adı da.
func TestSearchMatchesSubjectBodyAndAddress(t *testing.T) {
	api, srv := newAPI(t)

	send(t, srv, "magaza@x.test", []string{"kerim@y.test"},
		"Subject: Sipariş onayı\r\n\r\nSipariş numarası: 40771\r\n")
	send(t, srv, "bulten@x.test", []string{"ayse@y.test"},
		"Subject: Haftalık bülten\r\n\r\nyeni ürünler\r\n")
	waitFor(t, srv, 2)

	cases := map[string]int{
		"":         2,
		"sipariş":  1, // konu
		"40771":    1, // gövde
		"ayse":     1, // alıcı
		"bulten@x": 1, // gönderen
		"bülten":   1,
		"yokböyle": 0,
	}
	for q, want := range cases {
		resp, err := http.Get(api.URL + "/api/messages?q=" + url.QueryEscape(q))
		if err != nil {
			t.Fatal(err)
		}
		var got []Summary
		json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if len(got) != want {
			t.Errorf("q=%q: %d sonuç, beklenen %d", q, len(got), want)
		}
	}
}

// Türkçe'de strings.ToLower yetmiyor: "İPTAL" küçültülünce araya
// birleşen bir nokta giriyor ve "iptal" ile eşleşmiyor.
func TestSearchIsCaseInsensitiveInTurkish(t *testing.T) {
	api, srv := newAPI(t)
	send(t, srv, "a@x.test", []string{"b@y.test"},
		"Subject: =?utf-8?b?xLBQVEFMIEVESUxEasSw?=\r\n\r\nsipariş iptal edildi\r\n")
	waitFor(t, srv, 1)

	for _, q := range []string{"iptal", "İPTAL", "İptal", "IPTAL"} {
		resp, err := http.Get(api.URL + "/api/messages?q=" + url.QueryEscape(q))
		if err != nil {
			t.Fatal(err)
		}
		var got []Summary
		json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if len(got) != 1 {
			t.Errorf("q=%q: %d sonuç, 1 bekleniyordu", q, len(got))
		}
	}
}

// Röle, yalnız açıkça izin verilen alıcılara posta gönderir. Geri kalan
// her şey yalnız yakalanır: geliştirme ortamındaki test verisinde gerçek
// bir adres bulunması an meselesi.
func TestRelayOnlySendsToAllowedRecipients(t *testing.T) {
	var mu sync.Mutex
	var sent [][]string

	relay := &Relayer{
		Host:  "smtp.example.com:587",
		Allow: []string{"sirket.com", "kerim@baska.test"},
		send: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, to)
			return nil
		},
	}
	if err := relay.Validate(); err != nil {
		t.Fatal(err)
	}

	srv := newServer(t)
	srv.Relay = relay

	// Biri izinli alan adında, biri izinli tam adres, ikisi değil.
	send(t, srv, "uygulama@devbox.test",
		[]string{"ayse@sirket.com", "kerim@baska.test", "yabanci@baskayer.test"},
		"Subject: duyuru\r\n\r\ngövde\r\n")
	waitFor(t, srv, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("%d gönderim yapıldı, 1 bekleniyordu", len(sent))
	}
	got := strings.Join(sent[0], ",")
	if got != "ayse@sirket.com,kerim@baska.test" {
		t.Errorf("röle edilen alıcılar = %q; izinsiz alıcıya posta gitmiş olabilir", got)
	}
}

// Hiçbir alıcı listede değilse hiç bağlantı kurulmamalı.
func TestRelaySkipsWhenNoRecipientAllowed(t *testing.T) {
	var çağrıldı bool
	relay := &Relayer{
		Host:  "smtp.example.com:587",
		Allow: []string{"sirket.com"},
		send: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			çağrıldı = true
			return nil
		},
	}
	msg := Parse([]byte("Subject: x\r\n\r\ngövde\r\n"), "a@x.test", []string{"b@y.test"})
	if result := relay.Relay(msg); result != nil {
		t.Errorf("izinsiz alıcı için röle sonucu üretildi: %+v", result)
	}
	if çağrıldı {
		t.Error("izinsiz alıcı için SMTP bağlantısı kuruldu")
	}
}

// Alt alan adları kapsanmıyor: "sirket.com" yazan biri
// "test.sirket.com"a posta gitmesini istemiş sayılmaz.
func TestRelayDoesNotMatchSubdomains(t *testing.T) {
	relay := &Relayer{Host: "h:25", Allow: []string{"sirket.com"}}
	for addr, want := range map[string]bool{
		"a@sirket.com":      true,
		"A@SIRKET.COM":      true,
		"a@test.sirket.com": false,
		"a@sirket.com.evil": false,
		"a@baskasirket.com": false,
	} {
		if got := relay.allowed(addr); got != want {
			t.Errorf("allowed(%q) = %v, beklenen %v", addr, got, want)
		}
	}
}

// İzin listesi olmayan bir röle yapılandırması reddedilmeli: "hepsine
// gönder" kısayolu, aracın var oluş sebebini ortadan kaldırırdı.
func TestRelayValidation(t *testing.T) {
	cases := map[string]*Relayer{
		"sunucu yok":          {Allow: []string{"a.com"}},
		"port yok":            {Host: "smtp.example.com", Allow: []string{"a.com"}},
		"izin yok":            {Host: "smtp.example.com:587"},
		"boş izin":            {Host: "smtp.example.com:587", Allow: []string{" "}},
		"parolasız kullanıcı": {Host: "smtp.example.com:587", Allow: []string{"a.com"}, Username: "u"},
	}
	for label, r := range cases {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: geçersiz röle yapılandırması kabul edildi", label)
		}
	}
	ok := &Relayer{Host: "smtp.example.com:587", Allow: []string{"a.com"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("geçerli yapılandırma reddedildi: %v", err)
	}
}

// Röle başarısız olsa bile posta yakalanmış olmalı ve sonucu arayüzde
// görünmeli: "gitti mi gitmedi mi" sorusu cevapsız kalmamalı.
func TestRelayFailureIsVisible(t *testing.T) {
	relay := &Relayer{
		Host:   "smtp.example.com:587",
		Allow:  []string{"sirket.com"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		send: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			return fmt.Errorf("bağlanılamadı")
		},
	}
	srv := newServer(t)
	srv.Relay = relay
	send(t, srv, "a@x.test", []string{"ayse@sirket.com"}, "Subject: x\r\n\r\ngövde\r\n")
	waitFor(t, srv, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, _ := srv.Store.Latest()
		if msg.Relay != nil {
			if msg.Relay.Error == "" {
				t.Fatal("başarısız röle hatasız görünüyor")
			}
			if s := srv.Store.List()[0]; s.Relayed {
				t.Error("başarısız röle, listede gönderilmiş gibi görünüyor")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("röle sonucu postaya yazılmadı")
}
