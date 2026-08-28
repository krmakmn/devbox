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
