package elevate

import "testing"

// Windows'ta süreçlere argüman dizisi değil tek bir dize geçilir; ayrıştırma
// alıcı tarafta yapılır. Kuralları yanlış uygulamak en iyi ihtimalle bozuk
// bir yol, en kötüsü komuta fazladan argüman eklenmesi demek — yani
// yükseltilmiş bir süreçte komut enjeksiyonu.
func TestQuoteArg(t *testing.T) {
	cases := map[string]string{
		"basit":              "basit",
		"":                   `""`,
		"boşluk var":         `"boşluk var"`,
		`C:\php\php.exe`:     `C:\php\php.exe`,
		`C:\Program Files\a`: `"C:\Program Files\a"`,
		// Sondaki ters bölü tek başına özel değil: CommandLineToArgvW'de
		// ters bölü yalnız tırnaktan ÖNCE anlam kazanır. Tırnaklamaya
		// gerek yok.
		`C:\dizin\`: `C:\dizin\`,
		// Ama argüman tırnaklanmak zorundaysa (boşluk var), kapanış
		// tırnağından önceki ters bölü ikilenmeli; yoksa tırnağı kaçırır
		// ve sonraki argümanlar bu argümanın içine akar.
		`C:\a b\`: `"C:\a b\\"`,
		// Değerin içindeki tırnak kaçırılmalı.
		`de"me`:      `"de\"me"`,
		`a\"b`:       `"a\\\"b"`,
		"sekme\tvar": "\"sekme\tvar\"",
	}
	for in, want := range cases {
		if got := quoteArg(in); got != want {
			t.Errorf("quoteArg(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func TestBuildCommandLine(t *testing.T) {
	got := buildCommandLine([]string{"privileged", "dns-install", "-namespace", ".test", "-server", "127.0.0.53"})
	want := "privileged dns-install -namespace .test -server 127.0.0.53"
	if got != want {
		t.Errorf("komut satırı = %q, beklenen %q", got, want)
	}

	// Enjeksiyon denemesi tek bir argüman olarak kalmalı.
	got = buildCommandLine([]string{"privileged", `.test" -x "kotu`})
	if got == `privileged .test" -x "kotu` {
		t.Errorf("tırnak kaçıran değer komut satırına sızdı: %q", got)
	}
	if got != `privileged ".test\" -x \"kotu"` {
		t.Errorf("komut satırı = %q", got)
	}
}

func TestIsElevatedDoesNotPanic(t *testing.T) {
	// Dönen değer ortama bağlı; burada yalnız çağrılabilir olduğunu
	// doğruluyoruz.
	_ = IsElevated()
}
