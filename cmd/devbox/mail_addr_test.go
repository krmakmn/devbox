package main

import (
	"net"
	"strconv"
	"testing"

	"github.com/krmakmn/devbox/internal/mail"
	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/project"
)

func oturum(t *testing.T, smtp string) *upSession {
	t.Helper()
	cfg := &project.Config{Name: "deneme", Domain: "deneme.test"}
	cfg.Mail.SMTP = smtp
	return &upSession{cfg: cfg, alloc: ports.New("127.0.0.1")}
}

// TestMailAddrBostaTercihEdileniVerir, alışılmış portun boşken aynen
// kullanıldığını doğruluyor. İlk proje 1025'i almalı: geliştiricilerin
// .env dosyalarında yazan port bu.
//
// Sabit 1025 yerine boş bulunmuş bir port veriliyor. Sabit yazsaydık
// test ürünü değil çevreyi ölçerdi — nitekim ilk yazdığımda makinede
// 1025'i tutan bir süreç yüzünden kırmızıya döndü.
func TestMailAddrBostaTercihEdileniVerir(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bos := ln.Addr().String()
	ln.Close()

	got, err := oturum(t, bos).mailAddr()
	if err != nil {
		t.Fatalf("mailAddr: %v", err)
	}
	if got != bos {
		t.Errorf("adres = %q, %q bekleniyordu", got, bos)
	}
}

// TestMailAddrVarsayilanPort, yapılandırma boşken alışılmış portun
// tercih edildiğini gösterir. Dönen portun tam olarak 1025 olmasını
// şart koşmuyoruz: o portu tutan bir şey varsa ayırıcının yukarı
// taraması doğru davranıştır.
func TestMailAddrVarsayilanPort(t *testing.T) {
	got, err := oturum(t, "").mailAddr()
	if err != nil {
		t.Fatalf("mailAddr: %v", err)
	}
	_, portStr, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("dönen adres çözülemedi: %q", got)
	}
	port, _ := strconv.Atoi(portStr)

	_, varsayilanStr, _ := net.SplitHostPort(mail.DefaultSMTPAddr)
	varsayilan, _ := strconv.Atoi(varsayilanStr)

	if port < varsayilan {
		t.Errorf("port %d, varsayılandan (%d) küçük", port, varsayilan)
	}
}

// TestMailAddrDoluPortuAtlar, gerçek kullanımda çıkan bir kusuru
// kilitliyor.
//
// Paylaşılan kenardan sonra birden çok proje aynı anda çalışıyor ve
// hepsi 1025'i istiyor. Eskiden ikinci proje "posta yakalayıcı
// başlatılamadı" deyip geçiyordu: uyarı vardı ama o projenin postaları
// hiç yakalanmıyordu. Artık ayırıcı yukarı tarayıp boş bir port
// buluyor — ilk proje alışılmış portu alıyor, sonrakiler bir sonrakini.
func TestMailAddrDoluPortuAtlar(t *testing.T) {
	// Tercih edilen portu gerçekten tutuyoruz; ayırıcı bağlanmayı
	// deneyerek karar verdiği için taklit yeterli olmazdı.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tutulan := ln.Addr().String()

	_, portStr, err := net.SplitHostPort(tutulan)
	if err != nil {
		t.Fatal(err)
	}
	tutulanPort, _ := strconv.Atoi(portStr)

	got, err := oturum(t, tutulan).mailAddr()
	if err != nil {
		t.Fatalf("mailAddr dolu portta hata verdi: %v", err)
	}
	_, gotPortStr, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("dönen adres çözülemedi: %q", got)
	}
	gotPort, _ := strconv.Atoi(gotPortStr)

	if gotPort == tutulanPort {
		t.Fatalf("dolu port (%d) döndü; başka bir port bekleniyordu", gotPort)
	}
	if gotPort < tutulanPort {
		t.Errorf("port %d, tercih edilenden (%d) küçük", gotPort, tutulanPort)
	}

	// Dönen adres gerçekten bağlanabilir olmalı; yoksa yakalayıcı
	// birazdan patlar ve kullanıcı yine postasız kalır.
	deneme, err := net.Listen("tcp", got)
	if err != nil {
		t.Fatalf("dönen adrese bağlanılamadı: %v", err)
	}
	deneme.Close()
}

func TestMailAddrBozukGirdiyiReddeder(t *testing.T) {
	for _, kotu := range []string{"portyok", "127.0.0.1:abc"} {
		if _, err := oturum(t, kotu).mailAddr(); err == nil {
			t.Errorf("%q kabul edildi, reddedilmeliydi", kotu)
		}
	}
}

// TestProjectEnvPostaAdresiniTasiyor, PHP havuzunun ve süreçlerin doğru
// portu görmesini sağlayan bağlantıyı koruyor. Port artık sabit
// olmadığı için uygulamanın onu ortamdan öğrenmesi şart.
func TestProjectEnvPostaAdresiniTasiyor(t *testing.T) {
	u := oturum(t, "")
	srv := &mail.SMTPServer{Addr: "127.0.0.1:0", Store: mail.NewStore(10)}
	if err := srv.Start(); err != nil {
		t.Fatalf("sahte SMTP başlatılamadı: %v", err)
	}
	defer srv.Close()
	u.mailSMTP = srv

	env := u.projectEnv()
	_, port, err := net.SplitHostPort(srv.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	if env["MAIL_PORT"] != port {
		t.Errorf("MAIL_PORT = %q, %q bekleniyordu", env["MAIL_PORT"], port)
	}
	if env["MAIL_HOST"] == "" {
		t.Error("MAIL_HOST boş")
	}
}
