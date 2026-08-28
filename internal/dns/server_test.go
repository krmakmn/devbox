package dns

import (
	"context"
	"encoding/binary"
	"net"
	"sort"
	"testing"
	"time"
)

func newTestServer(t *testing.T, suffixes ...string) *Server {
	t.Helper()
	if len(suffixes) == 0 {
		suffixes = []string{"test"}
	}
	s := New(Config{Addr: "127.0.0.1:0", Suffixes: suffixes})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Go'nun kendi çözücüsünü sunucumuza yönlendiriyoruz: yanıtı bizden bağımsız
// yazılmış gerçek bir DNS istemcisi çözümlüyor. Kendi çözümleyicimizle test
// etseydik, aynı yanlış anlamayı iki yerde birden yapabilirdik.
func TestResolvesWithRealDNSClient(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, host := range []string{"magaza.test", "admin.magaza.test", "a.b.c.magaza.test", "test"} {
		addrs, err := s.Resolver().LookupHost(ctx, host)
		if err != nil {
			t.Errorf("%s çözümlenemedi: %v", host, err)
			continue
		}
		sort.Strings(addrs)
		found := false
		for _, a := range addrs {
			if a == "127.0.0.1" || a == "::1" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s → %v, loopback beklenirdi", host, addrs)
		}
	}
}

func TestRefusesForeignNames(t *testing.T) {
	// Açık çözücü olmamalıyız: kendi son ekimiz dışındaki hiçbir şeye
	// cevap vermiyoruz.
	s := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, host := range []string{"example.com", "google.com", "notatest"} {
		if _, err := s.Resolver().LookupHost(ctx, host); err == nil {
			t.Errorf("%s için yanıt verildi; reddedilmeliydi", host)
		}
	}
}

func TestRespondsOverTCP(t *testing.T) {
	// 512 baytı aşan yanıtlarda ve bazı çözücülerde TCP'ye düşülür.
	s := newTestServer(t)

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("TCP bağlantısı: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	q := buildQuery(0x1234, "magaza.test", typeA)
	var prefix [2]byte
	binary.BigEndian.PutUint16(prefix[:], uint16(len(q)))
	if _, err := conn.Write(append(prefix[:], q...)); err != nil {
		t.Fatal(err)
	}

	var lenBuf [2]byte
	if _, err := conn.Read(lenBuf[:]); err != nil {
		t.Fatalf("uzunluk okunamadı: %v", err)
	}
	reply := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := conn.Read(reply); err != nil {
		t.Fatalf("yanıt okunamadı: %v", err)
	}

	if id := binary.BigEndian.Uint16(reply[0:2]); id != 0x1234 {
		t.Errorf("yanıt kimliği %#x, beklenen 0x1234", id)
	}
	if ancount := binary.BigEndian.Uint16(reply[6:8]); ancount != 1 {
		t.Errorf("ANCOUNT = %d, beklenen 1", ancount)
	}
}

func TestResponseFlags(t *testing.T) {
	s := newTestServer(t)
	reply := s.Respond(buildQuery(1, "magaza.test", typeA))
	flags := binary.BigEndian.Uint16(reply[2:4])

	if flags&0x8000 == 0 {
		t.Error("QR biti yok; ileti yanıt olarak işaretlenmemiş")
	}
	if flags&0x0400 == 0 {
		t.Error("AA biti yok; kendi bölgemiz için yetkili olduğumuzu söylemiyoruz")
	}
	if flags&0x0080 != 0 {
		t.Error("RA biti var; özyineleme yapmadığımız hâlde yapıyormuş gibi görünüyoruz")
	}
	if rcode := uint8(flags & 0x000F); rcode != rcodeSuccess {
		t.Errorf("rcode = %d, beklenen 0", rcode)
	}
}

func TestUnknownTypeReturnsNoDataNotNXDomain(t *testing.T) {
	// Ad bizim ama MX kaydımız yok. Doğru cevap NOERROR + sıfır kayıt;
	// NXDOMAIN demek "böyle bir ad yok" demektir ve istemciyi yanıltır.
	s := newTestServer(t)
	reply := s.Respond(buildQuery(1, "magaza.test", 15 /* MX */))

	flags := binary.BigEndian.Uint16(reply[2:4])
	if rcode := uint8(flags & 0x000F); rcode != rcodeSuccess {
		t.Errorf("rcode = %d, beklenen NOERROR", rcode)
	}
	if ancount := binary.BigEndian.Uint16(reply[6:8]); ancount != 0 {
		t.Errorf("ANCOUNT = %d, beklenen 0", ancount)
	}
}

func TestRefusedRcodeForForeignName(t *testing.T) {
	s := newTestServer(t)
	reply := s.Respond(buildQuery(1, "example.com", typeA))
	flags := binary.BigEndian.Uint16(reply[2:4])
	if rcode := uint8(flags & 0x000F); rcode != rcodeRefused {
		t.Errorf("rcode = %d, beklenen REFUSED (%d)", rcode, rcodeRefused)
	}
}

func TestMultipleSuffixes(t *testing.T) {
	s := newTestServer(t, "test", "devbox")
	for _, name := range []string{"a.test", "b.devbox", "test", "devbox"} {
		if !s.owns(name) {
			t.Errorf("%s sahiplenilmedi", name)
		}
	}
	for _, name := range []string{"test.com", "devboxx", "atest"} {
		if s.owns(name) {
			t.Errorf("%s yanlışlıkla sahiplenildi", name)
		}
	}
}

func TestMalformedMessagesDoNotPanic(t *testing.T) {
	s := newTestServer(t)
	cases := [][]byte{
		nil,
		{},
		{0x00},
		make([]byte, headerSize), // soru yok
		append(make([]byte, headerSize), 0xC0, 0x0C), // soruda sıkıştırma işaretçisi
		append(make([]byte, headerSize), 0x40),       // etiket uzunluğu geçersiz
		append(make([]byte, headerSize), 0x05, 'a'),  // etiket kesik
	}
	for i, msg := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%d. bozuk ileti panikledi: %v", i, r)
				}
			}()
			s.Respond(msg)
		}()
	}
}

func TestNameLongerThanLimitIsRejected(t *testing.T) {
	s := newTestServer(t)
	msg := make([]byte, headerSize)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	// 63 baytlık etiketlerden 255 sınırını aşan bir ad kur.
	for i := 0; i < 6; i++ {
		msg = append(msg, 63)
		msg = append(msg, make([]byte, 63)...)
	}
	msg = append(msg, 0, 0, 1, 0, 1)

	if reply := s.Respond(msg); reply != nil {
		flags := binary.BigEndian.Uint16(reply[2:4])
		if rcode := uint8(flags & 0x000F); rcode == rcodeSuccess {
			t.Error("255 baytı aşan ad başarıyla cevaplandı")
		}
	}
}

// buildQuery, test için standart bir DNS sorgusu kurar.
func buildQuery(id uint16, name string, qtype uint16) []byte {
	msg := make([]byte, headerSize)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(msg[4:6], 1)      // QDCOUNT

	for _, label := range splitLabels(name) {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)

	var tail [4]byte
	binary.BigEndian.PutUint16(tail[0:2], qtype)
	binary.BigEndian.PutUint16(tail[2:4], classIN)
	return append(msg, tail[:]...)
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			if i > start {
				out = append(out, name[start:i])
			}
			start = i + 1
		}
	}
	return out
}
