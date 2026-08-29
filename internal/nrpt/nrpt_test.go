package nrpt

import (
	"strings"
	"testing"
)

func TestRuleValidation(t *testing.T) {
	ok := Rule{Namespace: ".test", Servers: []string{"127.0.0.53"}, Comment: DefaultComment}
	if err := ok.Validate(); err != nil {
		t.Fatalf("geçerli kural reddedildi: %v", err)
	}

	bad := []Rule{
		{Namespace: "test", Servers: []string{"127.0.0.1"}},  // nokta yok
		{Namespace: ".", Servers: []string{"127.0.0.1"}},     // çok kısa
		{Namespace: ".test", Servers: nil},                   // sunucu yok
		{Namespace: ".test", Servers: []string{"999.1.1.1"}}, // geçersiz IP
		{Namespace: ".test", Servers: []string{"localhost"}}, // IP değil
		{Namespace: ".a..b", Servers: []string{"127.0.0.1"}}, // çift nokta
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("geçersiz kural kabul edildi: %+v", r)
		}
	}
}

// Değerler PowerShell'e geçtiği için doğrulama kozmetik değil: tırnak
// kaçıran bir ad alanı komut enjeksiyonu demek.
func TestValidationBlocksCommandInjection(t *testing.T) {
	attacks := []string{
		".test'; Remove-Item C:\\ -Recurse; '",
		".test`nwhoami",
		".test$(whoami)",
		".test\"",
		".test;calc.exe",
		".test |curl evil",
	}
	for _, ns := range attacks {
		r := Rule{Namespace: ns, Servers: []string{"127.0.0.1"}}
		if err := r.Validate(); err == nil {
			t.Errorf("enjeksiyon denemesi kabul edildi: %q", ns)
		}
	}

	for _, srv := range []string{"127.0.0.1'; calc; '", "127.0.0.1 -x", "$(whoami)"} {
		r := Rule{Namespace: ".test", Servers: []string{srv}}
		if err := r.Validate(); err == nil {
			t.Errorf("enjeksiyon denemesi sunucu alanından geçti: %q", srv)
		}
	}
}

func TestAddScriptReplacesOldRules(t *testing.T) {
	script := addScript(Rule{
		Namespace: ".test",
		Servers:   []string{"127.0.0.53", "127.0.0.1"},
		Comment:   DefaultComment,
	})

	// Eski kuralları silmeden eklemek, her çalıştırmada bir kural daha
	// biriktirir ve çözümleme sırasını öngörülemez yapar.
	if !strings.Contains(script, "Remove-DnsClientNrptRule") {
		t.Error("betik eski kuralları temizlemiyor")
	}
	if !strings.Contains(script, "Add-DnsClientNrptRule") {
		t.Error("betik kural eklemiyor")
	}
	if !strings.Contains(script, "-NameServers @('127.0.0.53','127.0.0.1')") {
		t.Errorf("sunucu listesi beklenen biçimde değil:\n%s", script)
	}
	// Silme yalnız DevBox'ın kendi kurallarını hedeflemeli; kullanıcının
	// elle eklediği kuralları silmek kabul edilemez.
	if !strings.Contains(script, "$_.Comment -eq '"+DefaultComment+"'") {
		t.Error("silme işlemi DevBox açıklamasıyla sınırlanmamış")
	}
	if !strings.Contains(script, "$ErrorActionPreference='Stop'") {
		t.Error("hata durumunda betik durmuyor; başarısızlık sessiz kalır")
	}
}

func TestRemoveScriptIsScopedToDevBox(t *testing.T) {
	script := removeScript(".test", DefaultComment)
	if !strings.Contains(script, "$_.Comment -eq '"+DefaultComment+"'") {
		t.Error("silme DevBox kurallarıyla sınırlı değil")
	}
	if strings.Contains(script, "Add-DnsClientNrptRule") {
		t.Error("silme betiği kural ekliyor")
	}
}

func TestParseRulesHandlesSingleObjectAndArray(t *testing.T) {
	// PowerShell'in ConvertTo-Json'u tek nesneyi dizi olarak sarmalamaz.
	single := []byte(`{"Namespace":".test","NameServers":"127.0.0.53","Comment":"DevBox yerel geliştirme"}`)
	rules, err := parseRules(single)
	if err != nil {
		t.Fatalf("tek nesne çözülemedi: %v", err)
	}
	if len(rules) != 1 || rules[0].Namespace != ".test" || len(rules[0].Servers) != 1 {
		t.Errorf("tek nesne yanlış çözüldü: %+v", rules)
	}

	array := []byte(`[{"Namespace":[".test"],"NameServers":["127.0.0.53","127.0.0.1"],"Comment":"x"},` +
		`{"Namespace":".dev","NameServers":"10.0.0.1","Comment":"y"}]`)
	rules, err = parseRules(array)
	if err != nil {
		t.Fatalf("dizi çözülemedi: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("%d kural çözüldü, beklenen 2", len(rules))
	}
	if len(rules[0].Servers) != 2 {
		t.Errorf("ilk kuralda %d sunucu, beklenen 2", len(rules[0].Servers))
	}
	if rules[1].Namespace != ".dev" {
		t.Errorf("ikinci kural ad alanı %q", rules[1].Namespace)
	}
}

func TestParseRulesRejectsGarbage(t *testing.T) {
	if _, err := parseRules([]byte("bu json değil")); err == nil {
		t.Error("bozuk JSON kabul edildi")
	}
}

// TestListScriptDuzTipeZorluyor, gerçek Windows koşusunda çıkan bir kusuru
// kilitliyor: "devbox dns status" kuralı buluyor ama sunucu listesini boş
// gösteriyordu. Get-DnsClientNrptRule'un NameServers alanı dize değil
// IPAddress nesnesi; Select-Object'ten geçip ConvertTo-Json'a verilince
// nesne olarak serileşiyor ve hiçbir dize biçimine çözülmüyor.
func TestListScriptDuzTipeZorluyor(t *testing.T) {
	script := listScript()

	if strings.Contains(script, "Select-Object") {
		t.Error("listScript hâlâ Select-Object kullanıyor; CIM alanları null'a düşebilir")
	}
	if !strings.Contains(script, "NameServers=@(") {
		t.Error("NameServers dizi olmaya zorlanmıyor")
	}
	if !strings.Contains(script, "Namespace=@(") {
		t.Error("Namespace dizi olmaya zorlanmıyor")
	}
	if !strings.Contains(script, "-Depth") {
		t.Error("ConvertTo-Json derinliği açıkça verilmemiş; varsayılan 2 kırpıyor")
	}
}

// TestParseRulesBosSunucu, kusurun görünen belirtisini belgeliyor. Girdi
// gerçek koşudan alındı: NameServers bir IPAddress nesnesi olarak geliyor,
// kural okunuyor ama sunucusuz kalıyor. parseRules bunu hata saymıyor —
// çözüm betikte, burada değil.
func TestParseRulesBosSunucu(t *testing.T) {
	ham := []byte(`{"Namespace":[".test"],"NameServers":{"Address":889192575,` +
		`"AddressFamily":2,"IPAddressToString":"127.0.0.53"},` +
		`"Comment":"DevBox yerel geliştirme"}`)
	rules, err := parseRules(ham)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("kural sayısı %d, 1 bekleniyordu", len(rules))
	}
	if rules[0].Namespace != ".test" {
		t.Errorf("ad alanı %q", rules[0].Namespace)
	}
	if len(rules[0].Servers) != 0 {
		t.Errorf("sunucu listesi %v, boş bekleniyordu", rules[0].Servers)
	}
}
