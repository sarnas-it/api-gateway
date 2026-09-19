package proxy

import (
	"context"

	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
)

// RateLimiter — интерфейс лимитера роута. Реализуют встроенный IPRateLimiter
// и pluginRateLimiter (состояние в плагине).
type RateLimiter interface {
	Allow(ip string) bool
	Stop()
}

// pluginRateLimiter перекладывает решение о токене на плагин (fail closed).
type pluginRateLimiter struct {
	client ratelimitv1.RateLimitServiceClient
	key    string
	rate   float64
	burst  int
}

func newPluginRateLimiter(client ratelimitv1.RateLimitServiceClient, key string, rate float64, burst int) *pluginRateLimiter {
	return &pluginRateLimiter{client: client, key: key, rate: rate, burst: burst}
}

func (p *pluginRateLimiter) Allow(ip string) bool {
	resp, err := p.client.Allow(context.Background(), &ratelimitv1.AllowRequest{
		RouteKey: p.key, Ip: ip, Rate: p.rate, Burst: int64(p.burst),
	})
	if err != nil {
		return false
	}
	return resp.Allowed
}

func (p *pluginRateLimiter) Stop() {}
