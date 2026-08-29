package projects

import (
	"strings"
	"testing"
)

func TestEndpointGidipGeliyor(t *testing.T) {
	istenen := Endpoint{
		Addr:      "127.0.0.1:54321",
		Hosts:     []string{"magaza.test", "www.magaza.test"},
		LocalOnly: []string{"mail.magaza.test", "inspect.magaza.test"},
	}
	line, err := FormatEndpoint(istenen)
	if err != nil {
		t.Fatalf("FormatEndpoint: %v", err)
	}
	if !strings.HasPrefix(line, EndpointPrefix) {
		t.Fatalf("satır önekle başlamıyor: %q", line)
	}

	got, ok := ParseEndpoint("başka bir satır\n" + line + "\nsonra başka satır")
	if !ok {
		t.Fatal("bildirim çözülemedi")
	}
	if got.Addr != istenen.Addr {
		t.Errorf("adres = %q", got.Addr)
	}
	if strings.Join(got.Hosts, ",") != strings.Join(istenen.Hosts, ",") {
		t.Errorf("alan adları = %v", got.Hosts)
	}
	if strings.Join(got.LocalOnly, ",") != strings.Join(istenen.LocalOnly, ",") {
		t.Errorf("yerel alan adları = %v", got.LocalOnly)
	}
}

// TestEndpointSonSatiriAlir, gerçek bir tuzağı kilitliyor: denetçi
// çöken bir projeyi yeniden başlatıyor ve günlük tamponu önceki koşuyu
// da tutuyor. İlk satırı alsaydık, yeniden başlatmadan sonra kenar
// ölmüş bir adrese yönlendirirdi — site açılıyor görünür, bağlantı
// reddedilirdi.
func TestEndpointSonSatiriAlir(t *testing.T) {
	eski, _ := FormatEndpoint(Endpoint{Addr: "127.0.0.1:1111", Hosts: []string{"a.test"}})
	yeni, _ := FormatEndpoint(Endpoint{Addr: "127.0.0.1:2222", Hosts: []string{"a.test"}})

	got, ok := ParseEndpoint(eski + "\nçöktü, yeniden başlatılıyor\n" + yeni + "\n")
	if !ok {
		t.Fatal("bildirim çözülemedi")
	}
	if got.Addr != "127.0.0.1:2222" {
		t.Errorf("adres = %q, son bildirim bekleniyordu", got.Addr)
	}
}

func TestEndpointBozukGirdiyiReddeder(t *testing.T) {
	durumlar := []struct {
		ad    string
		girdi string
	}{
		{"bildirim yok", "sıradan günlük satırları\nbaşka bir satır"},
		{"json değil", EndpointPrefix + "bu json değil"},
		{"adres boş", EndpointPrefix + `{"hosts":["a.test"]}`},
		{"alan adı yok", EndpointPrefix + `{"addr":"127.0.0.1:1"}`},
	}
	for _, d := range durumlar {
		if _, ok := ParseEndpoint(d.girdi); ok {
			t.Errorf("%s: kabul edildi, reddedilmeliydi", d.ad)
		}
	}
}

func TestAllHosts(t *testing.T) {
	e := Endpoint{
		Hosts:     []string{"a.test"},
		LocalOnly: []string{"mail.a.test"},
	}
	got := strings.Join(e.AllHosts(), ",")
	if got != "a.test,mail.a.test" {
		t.Errorf("AllHosts = %q", got)
	}
}
