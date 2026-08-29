package main

import (
	"reflect"
	"testing"
)

// Bu, gerçek bir hatanın testi: flag paketi ilk konumsal argümandan sonra
// ayrıştırmayı durdurduğu için "dns serve -addr 127.0.0.1:15353" komutunda
// -addr sessizce yok sayılıyor, sunucu varsayılan adrese bağlanıyordu.
func TestSplitSubcommand(t *testing.T) {
	cases := []struct {
		args     []string
		wantSub  string
		wantRest []string
	}{
		{nil, "status", nil},
		{[]string{}, "status", []string{}},
		{[]string{"serve"}, "serve", []string{}},
		{[]string{"serve", "-addr", "127.0.0.1:53"}, "serve", []string{"-addr", "127.0.0.1:53"}},
		{[]string{"-verbose"}, "status", []string{"-verbose"}},
		// Bayrağın değeri alt komut sanılmamalı. Alt komut baştaysa alınır,
		// değilse varsayılana düşülür ve çağıran artık argümanı görüp hata
		// verir — sessizce yanlış alt komutu çalıştırmaktansa.
		{[]string{"-addr", "x", "-suffix", "test"}, "status", []string{"-addr", "x", "-suffix", "test"}},
		{[]string{"-addr", "127.0.0.1:53", "serve"}, "status", []string{"-addr", "127.0.0.1:53", "serve"}},
	}

	for _, c := range cases {
		sub, rest := splitSubcommand(c.args, "status")
		if sub != c.wantSub {
			t.Errorf("splitSubcommand(%v) alt komut = %q, beklenen %q", c.args, sub, c.wantSub)
		}
		if !reflect.DeepEqual(rest, c.wantRest) {
			t.Errorf("splitSubcommand(%v) kalan = %v, beklenen %v", c.args, rest, c.wantRest)
		}
	}
}

func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":               nil,
		"test":           {"test"},
		"test,dev":       {"test", "dev"},
		" test , dev , ": {"test", "dev"},
		",,":             nil,
	}
	for in, want := range cases {
		if got := splitList(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitList(%q) = %v, beklenen %v", in, got, want)
		}
	}
}

func TestSplitPort(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"127.0.0.1:8080", "127.0.0.1", "8080"},
		{"127.0.0.53:53", "127.0.0.53", "53"},
		{":443", "", "443"},
		{"localhost", "localhost", ""},
	}
	for _, c := range cases {
		host, port := splitPort(c.in)
		if host != c.host || port != c.port {
			t.Errorf("splitPort(%q) = %q,%q; beklenen %q,%q", c.in, host, port, c.host, c.port)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"mysqld", []string{"mysqld"}},
		{"mysqld --port 3307", []string{"mysqld", "--port", "3307"}},
		{"  boşluklar   arada  ", []string{"boşluklar", "arada"}},
		// Windows yollarındaki ters bölü kaçış sayılmamalı; aksi hâlde
		// C:\php\php.exe yazılamaz.
		{`C:\php\php-cgi.exe -b 127.0.0.1:9000`, []string{`C:\php\php-cgi.exe`, "-b", "127.0.0.1:9000"}},
		// Tırnak, boşluk içeren yolları korur.
		{`"C:\Program Files\MySQL\mysqld.exe" --defaults-file="C:\a b\my.ini"`,
			[]string{`C:\Program Files\MySQL\mysqld.exe`, `--defaults-file=C:\a b\my.ini`}},
		// Boş tırnak bir argümandır (boş dize geçirmek isteyen olabilir).
		{`komut ""`, []string{"komut", ""}},
	}

	for _, c := range cases {
		got, err := splitArgs(c.in)
		if err != nil {
			t.Errorf("splitArgs(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitArgs(%q) = %#v, beklenen %#v", c.in, got, c.want)
		}
	}

	if _, err := splitArgs(`komut "kapanmamış`); err == nil {
		t.Error("kapatılmamış tırnak kabul edildi")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"magaza":       "magaza",
		"my-project_2": "my-project_2",
		"proje adı":    "proje-adı",
		"mağaza":       "mağaza",
		"çiçek_2":      "çiçek_2",
		`C:\yol\proje`: "C--yol-proje",
		"":             "proje",
		"../..":        "-----",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// Bu kalıp iki kez hataya yol açtı (devbox logs, devbox db create): flag
// paketi ilk konumsal argümanda durduğu için, alt komuttan sonra gelen ad
// bayrakların ayrıştırılmasını engelliyordu.
func TestSplitNameAndFlags(t *testing.T) {
	cases := []struct {
		args     []string
		wantName string
		wantRest []string
	}{
		{[]string{"magaza", "-engine", "postgres"}, "magaza", []string{"-engine", "postgres"}},
		{[]string{"-engine", "postgres", "magaza"}, "", []string{"-engine", "postgres", "magaza"}},
		{[]string{"magaza"}, "magaza", []string{}},
		{nil, "", nil},
	}
	for _, c := range cases {
		name, rest := splitNameAndFlags(c.args)
		if name != c.wantName || !reflect.DeepEqual(rest, c.wantRest) {
			t.Errorf("splitNameAndFlags(%v) = %q,%v; beklenen %q,%v",
				c.args, name, rest, c.wantName, c.wantRest)
		}
	}
}
