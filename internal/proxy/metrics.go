package proxy

import (
	"expvar"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Metrics struct {
	requestsTotal    *expvar.Map
	requestDuration  *expvar.Map
	rateLimitDenials *expvar.Int
	targetUp         *expvar.Map
	activeRequests   int64
	mu               sync.RWMutex
}

func NewMetrics() *Metrics {
	m := &Metrics{
		requestsTotal:    expvar.NewMap("gateway_requests_total"),
		requestDuration:  expvar.NewMap("gateway_request_duration_ms"),
		rateLimitDenials: expvar.NewInt("gateway_rate_limit_denials_total"),
		targetUp:         expvar.NewMap("gateway_target_up"),
	}
	return m
}

func (m *Metrics) IncRequests(method, path, status string) {
	key := fmt.Sprintf("%s:%s:%s", method, path, status)
	m.requestsTotal.Add(key, 1)
}

func (m *Metrics) ObserveDuration(method, path string, d time.Duration) {
	key := fmt.Sprintf("%s:%s", method, path)
	ms := d.Milliseconds()
	m.requestDuration.Add(key, ms)
}

func (m *Metrics) IncRateLimitDenial() {
	m.rateLimitDenials.Add(1)
}

func (m *Metrics) SetTargetUp(name string, up bool) {
	val := int64(0)
	if up {
		val = 1
	}
	m.targetUp.Set(name, &expvar.Int{})
	m.targetUp.Add(name, val)
}

func (m *Metrics) IncActiveRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests++
}

func (m *Metrics) DecActiveRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests--
}

func (m *Metrics) Handler() http.Handler {
	expvar.NewInt("gateway_active_requests").Set(0)

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
