// Package cron, devbox.yaml'daki zamanlanmış görevleri çalıştırır.
//
// # Neden kendi ayrıştırıcımız
//
// Cron biçimi küçük ve donmuş: beş alan, birkaç işleç. Buna karşılık bir
// kütüphane getirmek, DevBox'ın tek dış bağımlılıkla yetinme kararını
// bozardı. Asıl mesele ayrıştırma değil zaten; gün-ay ve gün-hafta
// alanlarının birlikte kullanıldığında "veya" gibi davranması (Vixie
// cron'dan gelen davranış) gibi ince kuralları doğru uygulamak. Onu da
// testle sabitliyoruz.
//
// # Windows'ta neden gerek var
//
// Laragon'da zamanlanmış görev diye bir şey yok; Windows'un Görev
// Zamanlayıcısı ise depoya yazılamaz. Oysa Laravel'in "schedule:run"u
// dakikada bir çalışmak zorunda. Görevin devbox.yaml'da durması, ekip
// arkadaşının klonlayıp "devbox up" demesiyle aynı zamanlamayı alması
// demek.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule, bir görevin ne zaman çalışacağını söyler.
type Schedule interface {
	// Next, verilen andan sonraki ilk çalışma zamanını döner.
	// Bir daha çalışmayacaksa sıfır zaman döner.
	Next(after time.Time) time.Time
}

// Spec, beş alanlı bir cron ifadesi.
type Spec struct {
	minute  uint64 // 0-59
	hour    uint64 // 0-23
	dom     uint64 // 1-31
	month   uint64 // 1-12
	dow     uint64 // 0-6, pazar = 0
	domStar bool
	dowStar bool
}

// everySchedule, "@every 30s" biçimi.
type everySchedule struct{ d time.Duration }

func (e everySchedule) Next(after time.Time) time.Time {
	return after.Add(e.d - time.Duration(after.Nanosecond())*time.Nanosecond)
}

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Parse, bir cron ifadesini çözer.
func Parse(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("boş zamanlama")
	}

	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("@every süresi çözülemedi: %w", err)
		}
		if d < time.Second {
			return nil, fmt.Errorf("@every en az 1 saniye olmalı: %v", d)
		}
		return everySchedule{d: d}, nil
	}
	if expanded, ok := macros[strings.ToLower(spec)]; ok {
		spec = expanded
	}

	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron ifadesi 5 alan olmalı, %d alan var: %q", len(fields), spec)
	}

	s := &Spec{}
	var err error
	if s.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("dakika alanı: %w", err)
	}
	if s.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("saat alanı: %w", err)
	}
	if s.dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("ayın günü alanı: %w", err)
	}
	if s.month, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("ay alanı: %w", err)
	}
	if s.dow, err = parseField(fields[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("haftanın günü alanı: %w", err)
	}
	// 7 de pazar: her iki yazımı da kabul ediyoruz.
	if s.dow&(1<<7) != 0 {
		s.dow |= 1 << 0
		s.dow &^= 1 << 7
	}

	s.domStar = isStar(fields[2])
	s.dowStar = isStar(fields[4])
	return s, nil
}

func isStar(field string) bool {
	return field == "*" || field == "?"
}

// parseField, tek bir alanı bit kümesine çevirir.
func parseField(field string, min, max int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		b, err := parseRange(part, min, max, names)
		if err != nil {
			return 0, err
		}
		bits |= b
	}
	if bits == 0 {
		return 0, fmt.Errorf("hiçbir değere karşılık gelmiyor: %q", field)
	}
	return bits, nil
}

func parseRange(part string, min, max int, names map[string]int) (uint64, error) {
	step := 1
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		var err error
		if step, err = strconv.Atoi(stepStr); err != nil || step < 1 {
			return 0, fmt.Errorf("geçersiz adım %q", stepStr)
		}
		part = base
	}

	var lo, hi int
	switch {
	case isStar(part):
		lo, hi = min, max
	default:
		loStr, hiStr, isRange := strings.Cut(part, "-")
		var err error
		if lo, err = parseValue(loStr, names); err != nil {
			return 0, err
		}
		if !isRange {
			hi = lo
			// "5/15" gibi yazımlarda başlangıçtan sona kadar adımlanır.
			if step > 1 {
				hi = max
			}
		} else if hi, err = parseValue(hiStr, names); err != nil {
			return 0, err
		}
	}

	if lo < min || hi > max || lo > hi {
		return 0, fmt.Errorf("%d-%d aralığı %d-%d dışında", lo, hi, min, max)
	}

	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

func parseValue(s string, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("geçersiz değer %q", s)
	}
	return v, nil
}

// Next, verilen andan sonraki ilk çalışma zamanını döner.
//
// Alanları teker teker ilerletiyoruz; dakika dakika denemek beş yıl ileri
// bakan bir ifadede milyonlarca adım demek olurdu.
func (s *Spec) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)

	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).AddDate(0, 1, 0)
			continue
		}
		if !s.matchesDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Add(time.Hour)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// matchesDay, gün eşleşmesini uygular.
//
// Vixie cron kuralı: ayın günü ve haftanın günü alanlarının ikisi de
// kısıtlıysa gün, ikisinden BİRİNE uyduğunda çalışır — "ve" değil "veya".
// Bu yüzden "0 0 1 * 1" her ayın biri ve her pazartesi demek.
func (s *Spec) matchesDay(t time.Time) bool {
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(t.Weekday())) != 0

	if s.domStar && s.dowStar {
		return true
	}
	if s.domStar {
		return dowMatch
	}
	if s.dowStar {
		return domMatch
	}
	return domMatch || dowMatch
}
