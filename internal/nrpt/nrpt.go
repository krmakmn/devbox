// Package nrpt, Windows'un Ad Çözümleme İlkesi Tablosu'na (Name Resolution
// Policy Table) kural ekler.
//
// NRPT, "şu son eke sahip adları şu sunucuya sor" diyebilmenin desteklenen
// yoludur. hosts dosyasına göre iki üstünlüğü var: joker çalışır
// (*.magaza.test için satır yazmak gerekmez) ve makinenin geri kalan DNS'i
// hiç etkilenmez — VPN, kurumsal çözücü, split-DNS hepsi olduğu gibi kalır.
//
// İki kısıt, tasarımı doğrudan belirliyor:
//
//   - NRPT kuralı yalnız bir sunucu IP'si alır, port taşıyamaz. Bu yüzden
//     çözücümüz 53 numaralı portta dinlemek zorunda.
//   - Kural eklemek yönetici hakkı ister. Yol haritasındaki ayrıcalıklı
//     yardımcı servisin işlerinden biri bu; şimdilik yükseltilmiş bir
//     kabuktan çalıştırmak gerekiyor.
package nrpt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Rule, tek bir NRPT kuralı.
type Rule struct {
	// Namespace, kuralın kapsadığı son ek. Nokta ile başlamalı: ".test"
	// hem test'i hem *.test'i kapsar.
	Namespace string

	// Servers, bu son ek için sorulacak DNS sunucularının IP'leri.
	Servers []string

	// Comment, kuralı DevBox'ın koyduğunu belli eden açıklama. Kaldırırken
	// kullanıcının elle eklediği kuralları ayırt etmemizi sağlar.
	Comment string
}

// DefaultComment, DevBox kurallarını işaretler.
const DefaultComment = "DevBox yerel geliştirme"

// Validate, kuralın makul olduğunu denetler.
//
// Bu denetimler kozmetik değil: PowerShell'e geçireceğimiz değerler
// olduğu için, doğrulamadan geçirmek komut enjeksiyonuna açık kapı bırakır.
func (r Rule) Validate() error {
	if !strings.HasPrefix(r.Namespace, ".") {
		return fmt.Errorf("nrpt: ad alanı nokta ile başlamalı: %q", r.Namespace)
	}
	if len(r.Namespace) < 2 {
		return fmt.Errorf("nrpt: ad alanı çok kısa: %q", r.Namespace)
	}
	if !validNamespace(r.Namespace) {
		return fmt.Errorf("nrpt: geçersiz ad alanı: %q", r.Namespace)
	}
	if len(r.Servers) == 0 {
		return fmt.Errorf("nrpt: en az bir sunucu gerekli")
	}
	for _, s := range r.Servers {
		if !validIPv4(s) {
			return fmt.Errorf("nrpt: geçersiz sunucu adresi: %q", s)
		}
	}
	return nil
}

// validNamespace, yalnız harf, rakam, tire ve nokta kabul eder.
func validNamespace(ns string) bool {
	for i := 0; i < len(ns); i++ {
		c := ns[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return false
		}
	}
	return !strings.Contains(ns, "..")
}

// validIPv4, noktalı dörtlü biçimini denetler.
func validIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		n := 0
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
			n = n*10 + int(p[i]-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// addScript, kuralı ekleyen PowerShell betiğini üretir.
//
// Aynı ad alanı için eski DevBox kuralları önce siliniyor: aksi hâlde her
// çalıştırmada bir kural daha birikir ve çözümleme sırası öngörülemez hâle
// gelir.
func addScript(r Rule) string {
	servers := make([]string, len(r.Servers))
	for i, s := range r.Servers {
		servers[i] = "'" + s + "'"
	}
	return fmt.Sprintf(
		"$ErrorActionPreference='Stop'; "+
			"Get-DnsClientNrptRule | Where-Object { $_.Namespace -eq '%s' -and $_.Comment -eq '%s' } | "+
			"ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }; "+
			"Add-DnsClientNrptRule -Namespace '%s' -NameServers @(%s) -Comment '%s'",
		r.Namespace, r.Comment,
		r.Namespace, strings.Join(servers, ","), r.Comment,
	)
}

// removeScript, DevBox'ın bu ad alanı için koyduğu kuralları siler.
func removeScript(namespace, comment string) string {
	return fmt.Sprintf(
		"$ErrorActionPreference='Stop'; "+
			"Get-DnsClientNrptRule | Where-Object { $_.Namespace -eq '%s' -and $_.Comment -eq '%s' } | "+
			"ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }",
		namespace, comment,
	)
}

// listScript, mevcut kuralları JSON olarak döker.
//
// Select-Object ile doğrudan ilerlemek yerine her alan tek tek düz tipe
// çevriliyor. Sebebi gerçek bir kusur: Windows koşucusunda "devbox dns
// status" kuralı buluyor ama sunucu listesini boş gösteriyordu, çünkü
// Get-DnsClientNrptRule'un döndürdüğü CIM nesnesinin NameServers alanı
// ConvertTo-Json'a null olarak düşüyor. @(...) ve "$_" ile zorlayınca
// serileştirme düz bir dizeye dizisine iniyor.
//
// -Depth de açıkça veriliyor: varsayılan 2, iç içe dizilerde sessizce
// kırpıyor.
func listScript() string {
	return "$ErrorActionPreference='Stop'; " +
		"Get-DnsClientNrptRule | ForEach-Object { [pscustomobject]@{" +
		"Namespace=@($_.Namespace | ForEach-Object { \"$_\" });" +
		"NameServers=@($_.NameServers | ForEach-Object { \"$_\" });" +
		"Comment=\"$($_.Comment)\"" +
		"} } | ConvertTo-Json -Depth 4 -Compress"
}

// parseRules, ConvertTo-Json çıktısını çözer.
//
// PowerShell tek nesneyi dizi olarak sarmalamaz; iki biçimi de kabul
// etmemiz gerekiyor.
func parseRules(data []byte) ([]Rule, error) {
	type raw struct {
		Namespace   json.RawMessage `json:"Namespace"`
		NameServers json.RawMessage `json:"NameServers"`
		Comment     string          `json:"Comment"`
	}

	var items []raw
	if err := json.Unmarshal(data, &items); err != nil {
		var single raw
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("nrpt: kural listesi çözülemedi: %w", err)
		}
		items = []raw{single}
	}

	rules := make([]Rule, 0, len(items))
	for _, it := range items {
		rules = append(rules, Rule{
			Namespace: firstString(it.Namespace),
			Servers:   allStrings(it.NameServers),
			Comment:   it.Comment,
		})
	}
	return rules, nil
}

func firstString(raw json.RawMessage) string {
	if s := allStrings(raw); len(s) > 0 {
		return s[0]
	}
	return ""
}

func allStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	return nil
}
