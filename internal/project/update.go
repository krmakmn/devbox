package project

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Update, devbox.yaml metnine verilen alan değişikliklerini işler ve
// dokunmadığı her şeyi olduğu gibi bırakır.
//
// # Neden Config.Save değil
//
// Config'i çözüp yeniden serileştirmek dosyayı yeniden yazmak demek ve
// iki şeyi birden kaybettiriyor. Ölçüldü:
//
//	girdi:
//	  # Ekip notu: PHP 8.2'de kalıyoruz, 8.3 ile ödeme patlıyor.
//	  php:
//	    version: "8.2"   # yükseltmeden önce testleri koş
//
//	Save'den sonra: iki yorum da yok, üstelik yazılmamış varsayılanlar
//	(server, frontController) dosyaya sabitlenmiş.
//
// devbox.yaml depoya ekleniyor. Panelden yapılan tek bir kaydetme
// ekibin notlarını silip git'e gönderirdi. Bu yüzden düzenleme
// yaml.Node üzerinden, yalnız hedeflenen düğümlere dokunarak yapılıyor.
//
// Korunmayan tek şey boş satırlar: yaml.v3 onları düğümde tutmuyor.
// Yorumlar, sıra ve dokunulmayan alanlar korunuyor.
//
// Yollar noktayla ayrılıyor ("php.version"). Değer nil ise alan
// siliniyor. Sonuç, yazılmadan önce katı ayrıştırmadan geçiriliyor:
// DevBox'ın okuyamayacağı bir dosya asla diske yazılmıyor.
func Update(data []byte, changes map[string]any) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yapılandırma çözülemedi: %w", err)
	}

	root, err := documentRoot(&doc)
	if err != nil {
		return nil, err
	}

	// Yollar sıralı işleniyor: aynı girdi her zaman aynı çıktıyı
	// versin, harita gezinme sırası sonucu değiştirmesin.
	for _, path := range sortedKeys(changes) {
		if err := setPath(root, strings.Split(path, "."), changes[path]); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	// Girinti açıkça veriliyor: varsayılan 4, elle yazılmış dosyalar
	// neredeyse her zaman 2. Kaydetmenin bütün dosyayı yeniden
	// girintilemesi, git farkını okunamaz hâle getirirdi.
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := buf.Bytes()

	// Yazmadan önce hem çöz hem DOĞRULA. Yalnız çözmek yetmiyor:
	// Parse biçimi denetler, anlamı değil. "server: boyle-bir-sey-yok"
	// çözülür ama devbox up'ta reddedilir — kullanıcı paneli kaydeder,
	// sorunu ancak projeyi başlatmaya çalışınca görürdü.
	cfg, err := Parse(out)
	if err != nil {
		return nil, fmt.Errorf("düzenleme geçersiz bir yapılandırma üretti: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("düzenleme geçersiz bir yapılandırma üretti: %w", err)
	}
	return out, nil
}

func documentRoot(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("yapılandırma boş")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yapılandırmanın kökü eşleme olmalı")
	}
	return root, nil
}

// setPath, eşleme ağacında yolu izleyip değeri yazar. Ara düğümler
// yoksa oluşturulur.
func setPath(node *yaml.Node, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("boş alan yolu")
	}
	key := path[0]
	idx := findKey(node, key)

	if len(path) == 1 {
		if value == nil {
			if idx >= 0 {
				node.Content = append(node.Content[:idx], node.Content[idx+2:]...)
			}
			return nil
		}
		val, err := scalarNode(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if idx >= 0 {
			// Var olan düğümün yorumlarını koru: kullanıcının o satıra
			// yazdığı açıklama değeri değiştirdik diye silinmemeli.
			eski := node.Content[idx+1]
			val.HeadComment = eski.HeadComment
			val.LineComment = eski.LineComment
			val.FootComment = eski.FootComment
			node.Content[idx+1] = val
			return nil
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
		return nil
	}

	if idx < 0 {
		if value == nil {
			return nil // silinecek şey zaten yok
		}
		child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		return setPath(child, path[1:], value)
	}
	child := node.Content[idx+1]
	if child.Kind != yaml.MappingNode {
		return fmt.Errorf("%s bir eşleme değil; %q yazılamıyor", key, strings.Join(path, "."))
	}
	return setPath(child, path[1:], value)
}

// findKey, eşlemede anahtarın indeksini döner; yoksa -1.
func findKey(node *yaml.Node, key string) int {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// scalarNode, Go değerinden YAML düğümü üretir.
//
// Dizeler gerektiğinde tırnaklanıyor. Sebebi somut: PHP sürümü "8.3"
// tırnaksız yazılırsa YAML onu ondalık sayı olarak okur ve
// yapılandırma çözülemez.
func scalarNode(value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		if needsQuotes(v) {
			n.Style = yaml.DoubleQuotedStyle
		}
		return n, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}, nil
	case []string:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, s := range v {
			item, err := scalarNode(s)
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, item)
		}
		return seq, nil
	default:
		return nil, fmt.Errorf("desteklenmeyen değer türü: %T", value)
	}
}

// needsQuotes, dizenin tırnaksız yazılırsa başka bir türe kayıp
// kaymayacağını söyler.
func needsQuotes(s string) bool {
	if s == "" {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseBool(s); err == nil {
		return true
	}
	switch strings.ToLower(s) {
	case "null", "~", "yes", "no", "on", "off":
		return true
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Kısa yollar önce: "php" silinip sonra "php.version" yazılmasın.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
