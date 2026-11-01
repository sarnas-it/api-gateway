// Package file is an xgate discovery module that reads a JSON file with
// services and polls it for changes. Полезен, когда реестр сервисов уже
// выгружается в файл (sidecar, CI, ansible), а Docker API недоступен.
package file

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

// service — запись файла. Формат:
//
//	[{"name":"api","address":"10.0.0.5:8080","path_prefix":"/api","strip_path":true}]
type service struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	PathPrefix string `json:"path_prefix"`
	StripPath  bool   `json:"strip_path"`
}

type module struct {
	path     string
	interval time.Duration
}

func (*module) Name() string { return "file" }

func (m *module) Configure(cfg map[string]string) error {
	m.path = cfg["path"]
	if m.path == "" {
		return errors.New("path is required")
	}
	m.interval = 5 * time.Second
	if raw := cfg["interval"]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("interval: %w", err)
		}
		if d < 100*time.Millisecond {
			return errors.New("interval must be at least 100ms")
		}
		m.interval = d
	}
	return nil
}

// Watch опрашивает файл и вызывает emit только при изменении содержимого.
// Ошибки чтения/разбора не фатальны: остаётся последнее удачное состояние,
// чтобы полузаписанный файл не сносил маршруты.
func (m *module) Watch(ctx context.Context, emit func([]plugin.DiscoveredService)) error {
	var (
		last    [sha256.Size]byte
		haveRun bool
	)
	check := func() {
		data, err := os.ReadFile(m.path)
		if err != nil {
			log.Printf("xgate discovery file: read %s: %v", m.path, err)
			return
		}
		sum := sha256.Sum256(data)
		if haveRun && sum == last {
			return
		}
		var entries []service
		if err := json.Unmarshal(data, &entries); err != nil {
			log.Printf("xgate discovery file: parse %s: %v", m.path, err)
			return
		}
		services := make([]plugin.DiscoveredService, 0, len(entries))
		for _, e := range entries {
			services = append(services, plugin.DiscoveredService{
				Name:       e.Name,
				Address:    e.Address,
				PathPrefix: e.PathPrefix,
				StripPath:  e.StripPath,
			})
		}
		last, haveRun = sum, true
		emit(services)
	}

	check()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			check()
		}
	}
}

func init() { plugin.Register(&module{}) }
