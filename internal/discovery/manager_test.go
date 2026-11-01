package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"
	"go.uber.org/zap"
)

func TestManager_MergesBaseAndDiscovered(t *testing.T) {
	// Discovery не включаем: провайдер подменяется через setProvider, чтобы
	// тест не зависел от реального Docker-хоста.
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}

	updates := make(chan *config.Config, 10)
	m, err := NewManager(base, zap.NewNop(), func(c *config.Config) error { updates <- c; return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.setProvider(stubProvider{result: Result{
		Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}},
		Rules:   []config.RoutingRule{{PathPrefix: "/api/svc", TargetName: "svc"}},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-updates:
		if len(got.Targets) != 2 || len(got.Routing.Rules) != 1 {
			t.Fatalf("unexpected merged config: targets=%d rules=%d", len(got.Targets), len(got.Routing.Rules))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for update")
	}
	_ = m.Stop()
}

func TestManager_NoDuplicateUpdateForSameResult(t *testing.T) {
	// base валиден (есть target), иначе apply отбросит конфиг на валидации.
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}
	updates := atomic.Int64{}
	m, _ := NewManager(base, zap.NewNop(), func(*config.Config) error { updates.Add(1); return nil })
	m.setProvider(repeatProvider{result: Result{}, times: 3})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	if got := updates.Load(); got != 1 {
		t.Errorf("expected exactly 1 update for identical results, got %d", got)
	}
	_ = m.Stop()
}

type stubProvider struct {
	result Result
}

func (s stubProvider) Start(ctx context.Context, onResult func(Result)) error {
	onResult(s.result)
	<-ctx.Done()
	return nil
}

func (s stubProvider) Stop() error { return nil }

// TestManager_RetriesFailedUpdate проверяет, что неуспешный onUpdate не
// фиксируется как применённый и повторяется при следующем синке.
func TestManager_RetriesFailedUpdate(t *testing.T) {
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}
	var calls atomic.Int64
	m, err := NewManager(base, zap.NewNop(), func(*config.Config) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("reload failed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	res := Result{
		Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}},
		Rules:   []config.RoutingRule{{PathPrefix: "/api/svc", TargetName: "svc"}},
	}
	m.onResult(res)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected first update attempt, got %d calls", got)
	}
	// Тот же результат: первая попытка упала, значит должна быть повторена.
	m.onResult(res)
	if got := calls.Load(); got != 2 {
		t.Fatalf("failed update must be retried, got %d calls", got)
	}
	// После успеха повтор того же результата не дёргает колбэк.
	m.onResult(res)
	if got := calls.Load(); got != 2 {
		t.Fatalf("no update expected after success, got %d calls", got)
	}
}

// TestManager_ApplyUsesLatestResult доказывает, что apply читает base/last под
// m.mu в момент применения: снимок, сделанный SetBase до входа в apply, может
// оказаться устаревшим, если параллельно пришёл более свежий onResult.
func TestManager_ApplyUsesLatestResult(t *testing.T) {
	base1 := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}
	delivered := make(chan *config.Config, 4)
	m, err := NewManager(base1, zap.NewNop(), func(c *config.Config) error {
		delivered <- c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Удерживаем applyMu, чтобы SetBase обновил base, но не успел применить.
	m.applyMu.Lock()
	base2 := &config.Config{
		Targets: []config.TargetConfig{{Name: "static2", URL: "http://static2:1"}},
	}
	done := make(chan struct{})
	go func() {
		m.SetBase(base2)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		got := m.base
		m.mu.Unlock()
		if got == base2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SetBase did not update base")
		}
		time.Sleep(time.Millisecond)
	}

	// Пока apply заблокирован, discovery приносит более свежий результат.
	m.mu.Lock()
	m.last = Result{Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:1"}}}
	m.mu.Unlock()
	m.applyMu.Unlock()

	select {
	case got := <-delivered:
		found := false
		for _, tg := range got.Targets {
			if tg.Name == "svc" {
				found = true
			}
		}
		if !found {
			t.Fatalf("delivered config must reflect latest discovery result: %+v", got.Targets)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update delivered")
	}
	<-done
}

type repeatProvider struct {
	result Result
	times  int
}

func (s repeatProvider) Start(ctx context.Context, onResult func(Result)) error {
	for i := 0; i < s.times; i++ {
		onResult(s.result)
	}
	<-ctx.Done()
	return nil
}

func (s repeatProvider) Stop() error { return nil }

// TestManager_ApplySerialized доказывает, что onUpdate не вызывается повторно,
// пока не завершился предыдущий вызов: второй apply (SetBase) не должен войти в
// колбэк, пока первый (onResult) удерживает applyMu.
func TestManager_ApplySerialized(t *testing.T) {
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	var calls atomic.Int64
	m, err := NewManager(base, zap.NewNop(), func(*config.Config) error {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	go m.onResult(Result{
		Targets: []config.TargetConfig{{Name: "a", URL: "http://a:1"}},
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first onUpdate did not start")
	}

	secondDone := make(chan struct{})
	go func() {
		m.SetBase(&config.Config{
			Targets: []config.TargetConfig{{Name: "b", URL: "http://b:1"}},
		})
		close(secondDone)
	}()

	select {
	case <-entered:
		t.Fatal("second onUpdate started before the first finished")
	case <-time.After(50 * time.Millisecond):
	}

	unblock()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second onUpdate did not run after the first finished")
	}
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second apply did not complete")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 updates, got %d", got)
	}
}

// TestManager_LastJSONMatchesLastDelivered прогоняет конкурентные SetBase и
// onResult и проверяет, что onUpdate вызывается строго по одному, а lastJSON
// соответствует последнему доставленному конфигу.
func TestManager_LastJSONMatchesLastDelivered(t *testing.T) {
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}

	var inFlight, maxInFlight atomic.Int64
	var mu sync.Mutex
	var delivered []string

	m, err := NewManager(base, zap.NewNop(), func(c *config.Config) error {
		n := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if n <= old || maxInFlight.CompareAndSwap(old, n) {
				break
			}
		}
		data, err := json.Marshal(c)
		if err == nil {
			mu.Lock()
			delivered = append(delivered, string(data))
			mu.Unlock()
		}
		inFlight.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const iters = 40
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if j%2 == 0 {
					m.onResult(Result{
						Targets: []config.TargetConfig{{
							Name: fmt.Sprintf("svc-%d-%d", id, j),
							URL:  "http://svc:9000",
						}},
					})
					continue
				}
				m.SetBase(&config.Config{
					Targets: []config.TargetConfig{{
						Name: fmt.Sprintf("base-%d-%d", id, j),
						URL:  "http://base:1",
					}},
				})
			}
		}(i)
	}
	wg.Wait()

	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("onUpdate ran concurrently: max in-flight = %d", got)
	}

	mu.Lock()
	if len(delivered) == 0 {
		mu.Unlock()
		t.Fatal("no updates delivered")
	}
	last := delivered[len(delivered)-1]
	mu.Unlock()

	m.mu.Lock()
	lastJSON := m.lastJSON
	m.mu.Unlock()

	if lastJSON != last {
		t.Fatalf("lastJSON does not match last delivered config")
	}
}

func TestManager_StartTwiceIsNoop(t *testing.T) {
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}
	m, err := NewManager(base, zap.NewNop(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{entered: make(chan struct{})}
	m.setProvider(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
		t.Fatal("second Start started the provider again")
	case <-time.After(100 * time.Millisecond):
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("provider Start called %d times, want 1", got)
	}
	_ = m.Stop()
}

// TestManager_StopIsTerminal проверяет, что после Stop менеджер не
// перезапускает провайдер (терминальная семантика, согласованная с провайдером).
func TestManager_StopIsTerminal(t *testing.T) {
	base := &config.Config{
		Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}},
	}
	m, err := NewManager(base, zap.NewNop(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingProvider{entered: make(chan struct{})}
	m.setProvider(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
		t.Fatal("Start after Stop must not restart the provider")
	case <-time.After(100 * time.Millisecond):
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("provider Start called %d times, want 1", got)
	}
}

func TestManager_NilConfigReturnsError(t *testing.T) {
	m, err := NewManager(nil, zap.NewNop(), func(*config.Config) error { return nil })
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if m != nil {
		t.Fatalf("expected nil manager, got %v", m)
	}
}

type blockingProvider struct {
	calls   atomic.Int64
	entered chan struct{}
}

func (p *blockingProvider) Start(ctx context.Context, onResult func(Result)) error {
	p.calls.Add(1)
	p.entered <- struct{}{}
	<-ctx.Done()
	return nil
}

func (p *blockingProvider) Stop() error { return nil }

// TestManager_SaveAndLoadState проверяет атомарную запись и чтение состояния.
func TestManager_SaveAndLoadState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	m := &Manager{stateFile: stateFile, log: zap.NewNop()}
	res := Result{
		Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}},
		Rules:   []config.RoutingRule{{PathPrefix: "/api/svc", TargetName: "svc"}},
	}
	m.saveState(stateFile, res)
	m.loadState()

	m.mu.Lock()
	got := m.last
	m.mu.Unlock()
	if len(got.Targets) != 1 || got.Targets[0].Name != "svc" {
		t.Fatalf("unexpected loaded state: %+v", got)
	}
	if len(got.Rules) != 1 || got.Rules[0].PathPrefix != "/api/svc" {
		t.Fatalf("unexpected loaded rules: %+v", got.Rules)
	}
}

// TestManager_LoadState_CorruptIgnored проверяет, что повреждённый файл
// состояния не ломает менеджер — просто нет фолбэка.
func TestManager_LoadState_CorruptIgnored(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(stateFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{stateFile: stateFile, log: zap.NewNop()}
	m.loadState()
	m.mu.Lock()
	got := m.last
	m.mu.Unlock()
	if len(got.Targets) != 0 {
		t.Fatalf("corrupt state must be ignored, got %+v", got)
	}
}

// TestManager_AppliesPersistedStateOnStart доказывает аварийный фолбэк: при
// старте применяется последний сохранённый результат, даже если провайдер
// ничего не приносит (Docker недоступен).
func TestManager_AppliesPersistedStateOnStart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	res := Result{Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}}}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	base := &config.Config{Discovery: &config.DiscoveryConfig{
		Enabled:   true,
		Provider:  "docker",
		Host:      "unix:///var/run/docker.sock",
		StateFile: stateFile,
	}}
	delivered := make(chan *config.Config, 4)
	m, err := NewManager(base, zap.NewNop(), func(c *config.Config) error {
		delivered <- c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Провайдер блокируется и не приносит результатов — фолбэк должен сработать сам.
	m.setProvider(&blockingProvider{entered: make(chan struct{})})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-delivered:
		if len(got.Targets) != 1 || got.Targets[0].Name != "svc" {
			t.Fatalf("persisted state not applied: %+v", got.Targets)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update from persisted state")
	}
	_ = m.Stop()
}

// TestManager_PersistsResultOnOnResult проверяет, что каждый результат
// discovery сохраняется в state_file.
func TestManager_PersistsResultOnOnResult(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	base := &config.Config{Targets: []config.TargetConfig{{Name: "static", URL: "http://static:1"}}}
	m, err := NewManager(base, zap.NewNop(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.stateFile = stateFile
	m.mu.Unlock()

	m.onResult(Result{Targets: []config.TargetConfig{{Name: "svc", URL: "http://svc:9000"}}})

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].Name != "svc" {
		t.Fatalf("state not persisted: %+v", got)
	}
}

func TestManager_ModuleWithoutProviderFailsStart(t *testing.T) {
	cfg := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, Module: "file"}}
	m, err := NewManager(cfg, zap.NewNop(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatalf("NewManager must not build the docker provider for a module: %v", err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected error when discovery module is not wired")
	}
}

func TestManager_UseProviderAfterStart(t *testing.T) {
	cfg := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, Module: "file"}}
	m, err := NewManager(cfg, zap.NewNop(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.UseProvider(stubProvider{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.UseProvider(stubProvider{}); err == nil {
		t.Fatal("expected error when setting provider after Start")
	}
}
