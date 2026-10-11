package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// healthProbeTimeout — таймаут активной проверки одного upstream на /api/health.
const healthProbeTimeout = 3 * time.Second

// healthService — элемент агрегата здоровья upstream.
type healthService struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	HealthCheck string `json:"health_check,omitempty"`
	Healthy     bool   `json:"healthy"`
	Error       string `json:"error,omitempty"`
}

// healthEndpointMiddleware отдаёт агрегат здоровья upstream на GET /api/health.
//
// Статус каждого таргета проверяется пробой его health_check-URL на лету —
// эндпоинт работает независимо от фонового `application.health_check`
// (который на стенде выключен). Таргеты без health_check-URL считаются
// здоровыми по состоянию circuit breaker.
func healthEndpointMiddleware(mp *MultiProxy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/health" || r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			mp.serveHealth(w, r)
		})
	}
}

func (mp *MultiProxy) serveHealth(w http.ResponseWriter, r *http.Request) {
	mp.mu.RLock()
	targets := make([]*TargetProxy, 0, len(mp.targets))
	for _, tp := range mp.targets {
		targets = append(targets, tp)
	}
	mp.mu.RUnlock()

	services := make([]healthService, len(targets))
	var wg sync.WaitGroup
	for i, tp := range targets {
		wg.Add(1)
		go func(i int, tp *TargetProxy) {
			defer wg.Done()
			services[i] = probeTarget(r.Context(), tp)
		}(i, tp)
	}
	wg.Wait()

	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	status := "ok"
	for _, s := range services {
		if !s.Healthy {
			status = "degraded"
			break
		}
	}

	mp.setCORSHeaders(w.Header(), r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   status,
		"services": services,
	})
}

func probeTarget(ctx context.Context, tp *TargetProxy) healthService {
	svc := healthService{
		Name:        tp.config.Name,
		URL:         tp.config.URL,
		HealthCheck: tp.config.HealthCheck,
	}
	if tp.config.HealthCheck == "" {
		svc.Healthy = tp.isHealthy(false)
		return svc
	}

	cctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, tp.config.HealthCheck, nil)
	if err != nil {
		svc.Error = err.Error()
		return svc
	}
	client := &http.Client{Timeout: healthProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		svc.Error = err.Error()
		return svc
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	svc.Healthy = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !svc.Healthy {
		svc.Error = "status " + http.StatusText(resp.StatusCode)
	}
	return svc
}
