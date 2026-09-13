package proxy

import (
	"expvar"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Metrics собирает счётчики gateway. Когда enabled=false, все методы —
// это дешёвый no-op (без аллокаций ключей и без обращений к expvar),
// чтобы сбор метрик не стоил ничего на горячем пути, если он не включён
// в конфиге (application.metrics_enabled).
type Metrics struct {
	enabled          bool
	requestsTotal    *expvar.Map
	requestDuration  *expvar.Map
	rateLimitDenials *expvar.Int
	targetUp         *expvar.Map
	activeRequests   int64
	mu               sync.RWMutex
}

func NewMetrics(enabled bool) *Metrics {
	m := &Metrics{enabled: enabled}
	if !enabled {
		return m
	}
	m.requestsTotal = expvar.NewMap("gateway_requests_total")
	m.requestDuration = expvar.NewMap("gateway_request_duration_ms")
	m.rateLimitDenials = expvar.NewInt("gateway_rate_limit_denials_total")
	m.targetUp = expvar.NewMap("gateway_target_up")
	// Регистрируем один раз: повторный expvar.NewInt с тем же именем паникует,
	// а Handler() вызывается на каждый запрос /metrics.
	expvar.NewInt("gateway_active_requests")
	return m
}

func (m *Metrics) IncRequests(method, path, status string) {
	if !m.enabled {
		return
	}
	key := method + ":" + path + ":" + status
	m.requestsTotal.Add(key, 1)
}

func (m *Metrics) ObserveDuration(method, path string, d time.Duration) {
	if !m.enabled {
		return
	}
	key := method + ":" + path
	m.requestDuration.Add(key, d.Milliseconds())
}

func (m *Metrics) IncRateLimitDenial() {
	if !m.enabled {
		return
	}
	m.rateLimitDenials.Add(1)
}

func (m *Metrics) SetTargetUp(name string, up bool) {
	if !m.enabled {
		return
	}
	val := int64(0)
	if up {
		val = 1
	}
	m.targetUp.Set(name, &expvar.Int{})
	m.targetUp.Add(name, val)
}

func (m *Metrics) IncActiveRequests() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests++
}

func (m *Metrics) DecActiveRequests() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests--
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		expvar.Do(func(kv expvar.KeyValue) {
			if kv.Key == "gateway_active_requests" {
				m.mu.RLock()
				val := m.activeRequests
				m.mu.RUnlock()
				fmt.Fprintf(w, "%s %d\n", kv.Key, val)
				return
			}
			if kv.Key == "gateway_target_up" && kv.Value != nil {
				return
			}
			fmt.Fprintf(w, "%s %s\n", kv.Key, kv.Value.String())
		})
	})
}
