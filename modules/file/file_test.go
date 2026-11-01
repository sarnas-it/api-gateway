package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.sarnas.ru/api-gateway/pkg/plugin"
)

func TestConfigure(t *testing.T) {
	m := &module{}
	if err := m.Configure(map[string]string{}); err == nil {
		t.Fatal("path is required")
	}
	if err := m.Configure(map[string]string{"path": "/tmp/x", "interval": "nope"}); err == nil {
		t.Fatal("bad interval must fail")
	}
	if err := m.Configure(map[string]string{"path": "/tmp/x", "interval": "1ms"}); err == nil {
		t.Fatal("too small interval must fail")
	}
	if err := m.Configure(map[string]string{"path": "/tmp/x"}); err != nil {
		t.Fatal(err)
	}
	if m.interval != 5*time.Second {
		t.Fatalf("default interval = %v", m.interval)
	}
}

// collect запускает Watch и складывает события в канал. Интервал меньше
// минимума Configure задаётся напрямую: тест проверяет Watch, а не валидацию.
func collect(t *testing.T, path string) <-chan []plugin.DiscoveredService {
	t.Helper()
	m := &module{path: path, interval: 50 * time.Millisecond}
	ch := make(chan []plugin.DiscoveredService, 8)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = m.Watch(ctx, func(s []plugin.DiscoveredService) { ch <- s })
	}()
	t.Cleanup(cancel)
	return ch
}

func waitFor(t *testing.T, ch <-chan []plugin.DiscoveredService, want int) []plugin.DiscoveredService {
	t.Helper()
	select {
	case got := <-ch:
		if len(got) != want {
			t.Fatalf("services = %+v, want %d", got, want)
		}
		return got
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %d services", want)
		return nil
	}
}

func TestWatchEmitsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	if err := os.WriteFile(path, []byte(`[{"name":"api-1","address":"127.0.0.1:1","path_prefix":"/api"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := collect(t, path)

	first := waitFor(t, ch, 1)
	if first[0].Name != "api-1" || first[0].PathPrefix != "/api" {
		t.Fatalf("first = %+v", first)
	}

	// То же содержимое — повторных событий быть не должно.
	select {
	case got := <-ch:
		t.Fatalf("unexpected duplicate event: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Битый файл не сносит состояние и не эмитит.
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		t.Fatalf("broken file must not emit: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Валидное изменение — новое событие.
	if err := os.WriteFile(path, []byte(`[{"name":"api-1","address":"127.0.0.1:2"},{"name":"api-2","address":"127.0.0.1:3"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := waitFor(t, ch, 2)
	if second[1].Name != "api-2" {
		t.Fatalf("second = %+v", second)
	}
}

func TestWatchStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &module{path: path, interval: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Watch(ctx, func([]plugin.DiscoveredService) {}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not stop on cancel")
	}
}
