package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/basili4-1982/api-gateway/internal/config"
	"go.uber.org/zap"
)

// Manager связывает провайдер discovery со статическим конфигом: мёржит их и
// вызывает onUpdate только когда итоговый конфиг изменился.
type Manager struct {
	// mu защищает поля состояния менеджера. Не удерживается во время вызова
	// onUpdate (иначе Stop из колбэка приведёт к самоблокировке).
	mu        sync.Mutex
	base      *config.Config
	provider  Provider
	onUpdate  func(*config.Config) error
	log       *zap.Logger
	last      Result
	lastJSON  string
	cancel    context.CancelFunc
	started   bool
	stateFile string

	// applyMu сериализует всю последовательность merge→validate→dedup→onUpdate,
	// чтобы onUpdate вызывался строго по одному и по порядку, а lastJSON всегда
	// соответствовал последнему доставленному конфигу.
	applyMu sync.Mutex
}

// NewManager создаёт менеджер discovery. Если discovery выключен — возвращает
// менеджер с nil-провайдером (Start/Stop безопасны). onUpdate должен вернуть
// ошибку, если применение конфига не удалось: тогда оно будет повторено при
// следующем синке.
func NewManager(cfg *config.Config, log *zap.Logger, onUpdate func(*config.Config) error) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("discovery: config is nil")
	}
	m := &Manager{base: cfg, onUpdate: onUpdate, log: log}
	if cfg.Discovery == nil || !cfg.Discovery.Enabled {
		return m, nil
	}
	d := cfg.Discovery
	m.stateFile = d.StateFile
	provider, err := newDockerProvider(
		d.Host, d.APIVersion,
		ParseOptions{
			LabelPrefix:       d.LabelPrefix,
			ServiceNameLabels: d.ServiceNameLabels,
			DefaultTimeout:    d.DefaultTimeout,
			Network:           d.Network,
		},
		d.Debounce, d.ResyncInterval, log,
	)
	if err != nil {
		return nil, err
	}
	m.provider = provider
	return m, nil
}

// SetProvider заменяет провайдера discovery (например, на plugin-провайдер).
// Вызывать до Start.
func (m *Manager) SetProvider(p Provider) {
	m.setProvider(p)
}

// setProvider подменяет провайдер (для тестов).
func (m *Manager) setProvider(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = p
}

// Start запускает discovery. Безопасно вызывать при выключенном discovery.
// Повторный Start, пока провайдер уже запущен, — no-op.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	provider := m.provider
	if provider == nil {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	// Аварийный фолбэк: применяем последний сохранённый результат discovery,
	// чтобы маршруты были доступны сразу, даже если Docker/Podman недоступен.
	// Если провайдер затем успешно синхронизируется — состояние обновится.
	if m.loadState() {
		m.apply()
	}

	go func() {
		if err := provider.Start(ctx, m.onResult); err != nil && ctx.Err() == nil {
			m.log.Warn("discovery: provider stopped", zap.Error(err))
		}
	}()
	return nil
}

// SetBase обновляет статический конфиг (например, после SIGHUP) и пересобирает
// итоговый конфиг с последним результатом discovery.
func (m *Manager) SetBase(cfg *config.Config) {
	m.mu.Lock()
	m.base = cfg
	m.mu.Unlock()
	m.apply()
}

// Stop останавливает discovery. Остановка терминальна: dockerProvider.Stop
// необратим (повторный Start провайдера — no-op), поэтому started не
// сбрасывается и последующий Start менеджера тоже ничего не делает.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	provider := m.provider
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if provider != nil {
		return provider.Stop()
	}
	return nil
}

func (m *Manager) onResult(result Result) {
	m.mu.Lock()
	m.last = result
	stateFile := m.stateFile
	m.mu.Unlock()
	m.saveState(stateFile, result)
	m.apply()
}

// loadState читает последний сохранённый результат discovery из stateFile и
// кладёт его в m.last. Возвращает true, если состояние удалось загрузить.
// Ошибки/повреждённый файл не фатальны — просто нет фолбэка.
func (m *Manager) loadState() bool {
	m.mu.Lock()
	stateFile := m.stateFile
	m.mu.Unlock()
	if stateFile == "" {
		return false
	}
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			m.log.Warn("discovery: read state file failed", zap.String("path", stateFile), zap.Error(err))
		}
		return false
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		m.log.Warn("discovery: state file corrupt, ignoring", zap.String("path", stateFile), zap.Error(err))
		return false
	}
	m.mu.Lock()
	m.last = result
	m.mu.Unlock()
	m.log.Info("discovery: loaded persisted state",
		zap.String("path", stateFile),
		zap.Int("targets", len(result.Targets)),
		zap.Int("rules", len(result.Rules)),
	)
	return true
}

// saveState атомарно пишет результат discovery в stateFile (tmp + rename),
// чтобы аварийный фолбэк пережил рестарт. Пустой путь отключает персист.
func (m *Manager) saveState(stateFile string, result Result) {
	if stateFile == "" {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		m.log.Warn("discovery: marshal state failed", zap.Error(err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		m.log.Warn("discovery: create state dir failed", zap.String("path", stateFile), zap.Error(err))
		return
	}
	tmp := stateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		m.log.Warn("discovery: write state failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, stateFile); err != nil {
		m.log.Warn("discovery: rename state failed", zap.String("path", stateFile), zap.Error(err))
	}
}

func (m *Manager) apply() {
	// Сериализуем merge→validate→dedup→onUpdate целиком, чтобы SetBase (SIGHUP)
	// и onResult (горутина провайдера) не перемешивали lastJSON и не вызывали
	// onUpdate конкурентно. m.mu не удерживается во время колбэка.
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	// base и last читаются под m.mu в момент применения, а не снимаются
	// вызывающим: иначе устаревший снимок мог бы примениться последним.
	m.mu.Lock()
	base := m.base
	result := m.last
	m.mu.Unlock()

	merged := config.Merge(base, result.Targets, result.Rules)
	if err := merged.Validate(); err != nil {
		m.log.Warn("discovery: merged config invalid, keeping previous", zap.Error(err))
		return
	}

	normalized, err := json.Marshal(merged)
	if err != nil {
		m.log.Warn("discovery: marshal merged config failed", zap.Error(err))
		return
	}
	key := string(normalized)

	m.mu.Lock()
	if key == m.lastJSON {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	if err := m.onUpdate(merged); err != nil {
		// Не фиксируем lastJSON: следующий синк повторит применение.
		m.log.Warn("discovery: onUpdate failed, will retry", zap.Error(err))
		return
	}

	m.mu.Lock()
	m.lastJSON = key
	m.mu.Unlock()
}
