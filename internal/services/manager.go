package services

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/supervisor"
)

// Manager, yan servisleri başlatır ve ortam değişkenlerini toplar.
type Manager struct {
	// Root, servislerin veri dizinlerinin kökü.
	Root string

	// ExtraDirs, PATH'ten önce bakılacak dizinler (DevBox runtime'ları).
	ExtraDirs []string

	Supervisor *supervisor.Supervisor
	Alloc      *ports.Allocator
	Logger     *slog.Logger

	mu      sync.Mutex
	started []*Service
}

// Start, bir servisi ayağa kaldırır.
func (m *Manager) Start(ctx context.Context, spec Spec) (*Service, error) {
	driver, err := driverFor(spec.Kind)
	if err != nil {
		return nil, err
	}

	binary, err := findBinary(driver.Binaries(), m.ExtraDirs)
	if err != nil {
		return nil, fmt.Errorf("services: %s çalıştırılabiliri bulunamadı (%w).\n"+
			"  Kurulum: %s", spec.Kind, err, driver.InstallHint())
	}
	if spec.Version != "" {
		// Sürüm sabitleme runtime kayıt defterine bağlı; o altyapı
		// (imzalı manifest) henüz yok. Sessizce yanlış sürümü
		// çalıştırmak yerine söylüyoruz.
		m.logger().Warn("servis sürümü sabitlenemedi; makinede bulunan sürüm kullanılıyor",
			"servis", spec.Kind, "istenen", spec.Version, "çalıştırılabilir", binary)
	}

	dataDir := filepath.Join(m.Root, string(spec.Kind))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("services: veri dizini oluşturulamadı: %w", err)
	}

	svc := &Service{Spec: spec, Binary: binary, DataDir: dataDir}
	if svc.Port, err = m.Alloc.Allocate(driver.DefaultPort()); err != nil {
		return nil, fmt.Errorf("services: %s için port bulunamadı: %w", spec.Kind, err)
	}
	if driver.NeedsConsolePort() {
		if svc.ConsolePort, err = m.Alloc.Allocate(driver.DefaultPort() + 1); err != nil {
			return nil, fmt.Errorf("services: %s arayüz portu bulunamadı: %w", spec.Kind, err)
		}
	}

	service, err := m.Supervisor.Add(driver.ServiceConfig(svc))
	if err != nil {
		return nil, err
	}
	if err := service.Start(ctx); err != nil {
		return nil, fmt.Errorf("services: %s başlatılamadı: %w", spec.Kind, err)
	}

	m.mu.Lock()
	m.started = append(m.started, svc)
	m.mu.Unlock()

	m.logger().Info("yan servis hazır", "servis", spec.Kind, "port", svc.Port)
	return svc, nil
}

// Started, ayağa kalkmış servisler.
func (m *Manager) Started() []*Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Service(nil), m.started...)
}

// Env, çalışan servislerin uygulamaya vereceği değişkenler.
func (m *Manager) Env() map[string]string {
	env := make(map[string]string)
	for _, svc := range m.Started() {
		driver, err := driverFor(svc.Kind)
		if err != nil {
			continue
		}
		for k, v := range driver.Env(svc) {
			env[k] = v
		}
	}
	return env
}

// Summary, özet satırı için servis bilgisi.
func (s *Service) Summary() string {
	if s.ConsolePort != 0 {
		return fmt.Sprintf("%s: 127.0.0.1:%d (arayüz :%d)", s.Kind, s.Port, s.ConsolePort)
	}
	return fmt.Sprintf("%s: 127.0.0.1:%d", s.Kind, s.Port)
}

func (m *Manager) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}
