//go:build windows

package nrpt

import (
	"fmt"
	"os/exec"
	"strings"
)

// Supported, bu platformda NRPT kullanılabilir mi.
func Supported() bool { return true }

func run(script string) (string, error) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(strings.ToLower(text), "access is denied") ||
			strings.Contains(text, "Erişim engellendi") {
			return text, fmt.Errorf("nrpt: yönetici hakkı gerekli: %w", err)
		}
		return text, fmt.Errorf("nrpt: powershell başarısız: %w: %s", err, text)
	}
	return text, nil
}

// Add, kuralı ekler (aynı ad alanı için eski DevBox kurallarını değiştirerek).
func Add(r Rule) error {
	if r.Comment == "" {
		r.Comment = DefaultComment
	}
	if err := r.Validate(); err != nil {
		return err
	}
	_, err := run(addScript(r))
	return err
}

// Remove, DevBox'ın bu ad alanı için koyduğu kuralları siler.
func Remove(namespace string) error {
	r := Rule{Namespace: namespace, Servers: []string{"127.0.0.1"}, Comment: DefaultComment}
	if err := r.Validate(); err != nil {
		return err
	}
	_, err := run(removeScript(namespace, DefaultComment))
	return err
}

// List, makinedeki tüm NRPT kurallarını döner.
func List() ([]Rule, error) {
	out, err := run(listScript())
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return parseRules([]byte(out))
}
