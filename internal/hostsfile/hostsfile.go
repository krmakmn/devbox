// Package hostsfile, hosts dosyasındaki DevBox bloğunu yönetir.
//
// Bu, NRPT'nin geri düşüşüdür. NRPT daha iyisidir (joker çalışır, makinenin
// geri kalan DNS'ine dokunmaz) ama yönetici hakkı ister ve kurumsal ilkeyle
// engellenmiş olabilir. O durumda elimizde kalan tek şey hosts dosyası.
//
// Blok işaretçiler arasında bütün olarak üretilir ve değiştirilir; dışındaki
// hiçbir satıra dokunulmaz. Elle eklenmiş satırların arasına karışıp
// kullanıcının kendi girdilerini bozmamak için.
package hostsfile

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	beginMarker = "# >>> DevBox başlangıç — elle düzenlemeyin"
	endMarker   = "# <<< DevBox bitiş"
)

// Entry, tek bir hosts satırı.
type Entry struct {
	IP    string
	Names []string
}

// Path, işletim sisteminin hosts dosyasının yolu.
func Path() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

// Apply, DevBox bloğunu verilen girdilerle değiştirir. Blok yoksa dosyanın
// sonuna eklenir; girdi listesi boşsa blok tamamen kaldırılır.
func Apply(path string, entries []Entry) error {
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hostsfile: okunamadı: %w", err)
	}

	newline := detectNewline(original)
	before, after := splitAroundBlock(original)

	var out bytes.Buffer
	if len(entries) == 0 {
		out.Write(before)
	} else {
		// Blok her zaman kendi satırında başlasın; öncesindeki fazla boş
		// satırlar da birikmesin.
		trimmed := bytes.TrimRight(before, "\r\n")
		out.Write(trimmed)
		if len(trimmed) > 0 {
			out.WriteString(newline)
		}
		out.WriteString(beginMarker)
		out.WriteString(newline)
		for _, e := range entries {
			out.WriteString(e.IP)
			for _, n := range e.Names {
				out.WriteString(" ")
				out.WriteString(n)
			}
			out.WriteString(newline)
		}
		out.WriteString(endMarker)
		out.WriteString(newline)
	}
	out.Write(after)

	return writeAtomic(path, out.Bytes())
}

// Managed, blok içindeki girdileri döner.
func Managed(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []Entry
	inBlock := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == beginMarker:
			inBlock = true
			continue
		case line == endMarker:
			inBlock = false
			continue
		case !inBlock || line == "" || strings.HasPrefix(line, "#"):
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entries = append(entries, Entry{IP: fields[0], Names: fields[1:]})
	}
	return entries, scanner.Err()
}

// Remove, DevBox bloğunu tamamen kaldırır.
func Remove(path string) error { return Apply(path, nil) }

// splitAroundBlock, dosyayı DevBox bloğunun öncesi ve sonrası diye ayırır.
func splitAroundBlock(data []byte) (before, after []byte) {
	begin := bytes.Index(data, []byte(beginMarker))
	if begin < 0 {
		return data, nil
	}
	end := bytes.Index(data[begin:], []byte(endMarker))
	if end < 0 {
		// Bitiş işaretçisi kaybolmuş (elle silinmiş): başlangıçtan sonrasını
		// atmak yerine yalnız başlangıç satırını atıyoruz; kullanıcının
		// altına yazdığı satırları yok etmek kabul edilemez.
		return data[:begin], data[begin+len(beginMarker):]
	}
	end += begin + len(endMarker)
	// Bitiş işaretçisinden sonraki satır sonunu da yut.
	for end < len(data) && (data[end] == '\r' || data[end] == '\n') {
		end++
	}
	return data[:begin], data[end:]
}

// detectNewline, dosyanın kullandığı satır sonunu bulur. Windows'un hosts
// dosyası CRLF'tir; LF yazmak bazı eski araçları şaşırtıyor.
func detectNewline(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	if len(data) == 0 && runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// writeAtomic, dosyayı geçici bir dosya üzerinden değiştirir.
//
// hosts dosyası yarım yazılırsa makinenin ad çözümlemesi bozulur; bu, bir
// geliştirme aracının yapabileceği en kötü şeylerden biri.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".devbox-hosts-*")
	if err != nil {
		return fmt.Errorf("hostsfile: geçici dosya oluşturulamadı (yönetici hakkı gerekebilir): %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hostsfile: yerine konulamadı (yönetici hakkı ya da antivirüs engeli): %w", err)
	}
	return nil
}
