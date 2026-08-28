package runtime

import (
	"strconv"
	"strings"
)

// Version, "8.3.14" gibi noktalı sürüm numaralarını karşılaştırmak için.
//
// Tam semver uygulamıyoruz: PHP, Node ve PostgreSQL sürüm numaraları sayısal
// üçlülerden ibaret; ön sürüm etiketleri (8.4.0RC1) nadiren işimize giriyor ve
// onları da sıralamanın sonuna atmak yeterli.
type Version struct {
	parts []int
	// suffix, sayısal olmayan kuyruk ("RC1", "beta2"). Boşsa kararlı sürüm.
	suffix string
	raw    string
}

// ParseVersion, sürüm dizesini çözer. Hatalı girdi için hata dönmez;
// çözülemeyen kısım sonek sayılır, böylece beklenmedik bir sürüm biçimi
// kurulumu tümden bozmaz.
func ParseVersion(s string) Version {
	v := Version{raw: s}
	rest := strings.TrimSpace(s)

	for i, field := range strings.Split(rest, ".") {
		n, err := strconv.Atoi(field)
		if err == nil {
			v.parts = append(v.parts, n)
			continue
		}
		// "0RC1" gibi karışık alanı sayı ve sonek diye ayır.
		cut := 0
		for cut < len(field) && field[cut] >= '0' && field[cut] <= '9' {
			cut++
		}
		if cut > 0 {
			n, _ := strconv.Atoi(field[:cut])
			v.parts = append(v.parts, n)
		}
		v.suffix = strings.Join(append([]string{field[cut:]}, strings.Split(rest, ".")[i+1:]...), ".")
		break
	}
	return v
}

func (v Version) String() string { return v.raw }

// IsPrerelease, sürümün ön sürüm olup olmadığını söyler.
func (v Version) IsPrerelease() bool { return v.suffix != "" }

// Compare, v ile o'yu karşılaştırır: -1, 0 ya da 1.
//
// Kararlı sürüm, aynı sayılara sahip ön sürümden büyüktür: 8.4.0 > 8.4.0RC1.
func (v Version) Compare(o Version) int {
	n := len(v.parts)
	if len(o.parts) > n {
		n = len(o.parts)
	}
	for i := 0; i < n; i++ {
		a, b := partAt(v.parts, i), partAt(o.parts, i)
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}

	switch {
	case v.suffix == o.suffix:
		return 0
	case v.suffix == "":
		return 1 // kararlı, ön sürümden büyük
	case o.suffix == "":
		return -1
	case v.suffix < o.suffix:
		return -1
	default:
		return 1
	}
}

func partAt(parts []int, i int) int {
	if i < len(parts) {
		return parts[i]
	}
	return 0
}

// Matches, sürümün bir kısıta uyup uymadığını söyler.
//
// Kısıt bir önek: "8" tüm 8.x'i, "8.3" tüm 8.3.x'i, "8.3.14" yalnız o sürümü
// kapsar. Boş kısıt her şeye uyar. Sürüm aralıkları (>=, ^, ~) bilerek yok:
// yerel geliştirme ortamında "PHP 8.3 olsun" demek yetiyor ve karmaşık kısıt
// dili öğrenmek zorunda kalmak istemiyoruz.
func (v Version) Matches(constraint string) bool {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" || constraint == "*" || constraint == "latest" {
		return true
	}
	c := ParseVersion(constraint)
	if len(c.parts) == 0 {
		return false
	}
	for i, want := range c.parts {
		if partAt(v.parts, i) != want {
			return false
		}
	}
	// Kısıtta sonek varsa birebir eşleşmeli; yoksa ön sürümler elenir.
	if c.suffix != "" {
		return v.suffix == c.suffix
	}
	return v.suffix == ""
}

// SplitSpec, "php@8.3" biçimindeki belirteci ada ve sürüm kısıtına ayırır.
func SplitSpec(spec string) (name, constraint string) {
	name, constraint, found := strings.Cut(spec, "@")
	name = strings.TrimSpace(strings.ToLower(name))
	if !found {
		return name, ""
	}
	return name, strings.TrimSpace(constraint)
}
