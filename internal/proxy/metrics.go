package proxy

import (
	"expvar"
	"fmt"
	"net/http"
	"sync"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	targetRestUntil  *expvar.Map
	targetRestReason *expvar.Map
	activeRequests   int64
	mu               sync.RWMutex

	registry            *prometheus.Registry
	promRequests        *prometheus.CounterVec
	promDuration        *prometheus.HistogramVec
	promActive          prometheus.Gauge
	promDenials         prometheus.Counter
	promTargetUp        *prometheus.GaugeVec
	promTargetRestUntil *prometheus.GaugeVec
	promHandler         http.Handler
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
	m.targetRestUntil = expvar.NewMap("gateway_target_rest_until")
	m.targetRestReason = expvar.NewMap("gateway_target_rest_reason")
	// Регистрируем один раз: повторный expvar.NewInt с тем же именем паникует,
	// а Handler() вызывается на каждый запрос /metrics.
	expvar.NewInt("gateway_active_requests")

	m.registry = prometheus.NewRegistry()
	m.promRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total HTTP requests handled by the gateway.",
	}, []string{"method", "route", "status"})
	m.promDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "Gateway request duration in seconds.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"method", "route", "status"})
	m.promActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_active_requests",
		Help: "Requests currently being processed.",
	})
	m.promDenials = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_rate_limit_denials_total",
		Help: "Requests rejected by rate limiting.",
	})
	m.promTargetUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_target_up",
		Help: "Target health (1 = up, 0 = down).",
	}, []string{"target"})
	m.promTargetRestUntil = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_target_rest_until_seconds",
		Help: "Unix timestamp when the target returns to the pool after a 429/503 rest; a past value means the target is not resting.",
	}, []string{"target"})
	m.registry.MustRegister(m.promRequests, m.promDuration, m.promActive, m.promDenials, m.promTargetUp, m.promTargetRestUntil)
	m.promHandler = promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return m
}

func (m *Metrics) IncRequests(method, route, status string) {
	if !m.enabled {
		return
	}
	key := method + ":" + route + ":" + status
	m.requestsTotal.Add(key, 1)
	m.promRequests.WithLabelValues(method, route, status).Inc()
}

func (m *Metrics) ObserveDuration(method, route, status string, d time.Duration) {
	if !m.enabled {
		return
	}
	key := method + ":" + route
	m.requestDuration.Add(key, d.Milliseconds())
	m.promDuration.WithLabelValues(method, route, status).Observe(d.Seconds())
}

// routeLabel — значение метки route: имя маршрута (таргета) из конфига,
// который обслужил запрос.
//
// Путь из запроса для метки не годится: боты перебирают случайные URL, каждый
// новый первый сегмент (/wp-login.php, /.env, /<случайная строка>) добавлял
// новую серию на все четыре семейства gateway_*, включая 13 бакетов
// гистограммы. Кардинальность росла без предела и выедала
// -remoteWrite.maxHourlySeries у vmagent, из-за чего переставали скрапиться
// остальные таргеты. Маршрутов в конфиге конечное число, поэтому метка
// ограничена их набором; запрос без маршрута — "unmatched".
func routeLabel(rule *config.RoutingRule, target *config.TargetConfig) string {
	if rule != nil && rule.TargetName != "" {
		return rule.TargetName
	}
	if target != nil && target.Name != "" {
		return target.Name
	}
	return "unmatched"
}

func (m *Metrics) IncRateLimitDenial() {
	if !m.enabled {
		return
	}
	m.rateLimitDenials.Add(1)
	m.promDenials.Inc()
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
	m.promTargetUp.WithLabelValues(name).Set(float64(val))
}

// SetTargetRest публикует момент возврата отдыхающего таргета и причину
// отдыха. Gauge с датой в прошлом читается как «таргет не отдыхает» —
// отдельного сброса метрики по истечении не требуется.
func (m *Metrics) SetTargetRest(name string, until time.Time, reason string) {
	if !m.enabled {
		return
	}
	untilVar := new(expvar.Int)
	untilVar.Set(until.Unix())
	m.targetRestUntil.Set(name, untilVar)
	reasonVar := new(expvar.String)
	reasonVar.Set(reason)
	m.targetRestReason.Set(name, reasonVar)
	m.promTargetRestUntil.WithLabelValues(name).Set(float64(until.Unix()))
}

func (m *Metrics) IncActiveRequests() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests++
	m.promActive.Set(float64(m.activeRequests))
}

func (m *Metrics) DecActiveRequests() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRequests--
	m.promActive.Set(float64(m.activeRequests))
}

// Handler — прежний expvar-формат для дашборда и monitoring-glue.
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

// PromHandler — Prometheus-формат с гистограммами.
func (m *Metrics) PromHandler() http.Handler {
	if m.promHandler == nil {
		return http.NotFoundHandler()
	}
	return m.promHandler
}
