package proxy

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type IPRateLimiter struct {
	mu              sync.RWMutex
	limiters        map[string]*rate.Limiter
	lastSeen        map[string]time.Time
	rate            rate.Limit
	burst           int
	cleanupInterval time.Duration
	ttl             time.Duration
	stopCh          chan struct{}
}

func NewIPRateLimiter(r float64, burst int) *IPRateLimiter {
	l := &IPRateLimiter{
		limiters:        make(map[string]*rate.Limiter),
		lastSeen:        make(map[string]time.Time),
		rate:            rate.Limit(r),
		burst:           burst,
		cleanupInterval: 10 * time.Minute,
		ttl:             time.Hour,
		stopCh:          make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

func (l *IPRateLimiter) Stop() {
	close(l.stopCh)
}

func (l *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	l.mu.RLock()
	limiter, exists := l.limiters[ip]
	l.mu.RUnlock()

	if exists {
		return limiter
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	limiter, exists = l.limiters[ip]
	if exists {
		return limiter
	}

	limiter = rate.NewLimiter(l.rate, l.burst)
	l.limiters[ip] = limiter
	l.lastSeen[ip] = time.Now()

	return limiter
}

func (l *IPRateLimiter) Allow(ip string) bool {
	limiter := l.GetLimiter(ip)
	l.mu.Lock()
	l.lastSeen[ip] = time.Now()
	l.mu.Unlock()
	return limiter.Allow()
}

func (l *IPRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.stopCh:
			return
		}
	}
}

func (l *IPRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for ip, last := range l.lastSeen {
		if now.Sub(last) > l.ttl {
			delete(l.limiters, ip)
			delete(l.lastSeen, ip)
		}
	}
}

// clientIP возвращает клиентский IP с учётом текущего списка доверенных прокси.
func (mp *MultiProxy) clientIP(r *http.Request) string {
	mp.mu.RLock()
	trusted := mp.trustedProxies
	mp.mu.RUnlock()
	return getClientIP(r, trusted)
}

// getClientIP возвращает клиентский IP. X-Forwarded-For учитывается только
// если непосредственный пир (RemoteAddr) входит в trusted. Цепочка читается
// справа налево: первый недоверенный адрес — клиентский, всё левее —
// подделка. Без trusted XFF игнорируется.
func getClientIP(r *http.Request, trusted []*net.IPNet) string {
	remote := hostOnly(r.RemoteAddr)
	if len(trusted) == 0 || !ipTrusted(remote, trusted) {
		return remote
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	ips := strings.Split(xff, ",")
	for i := len(ips) - 1; i >= 0; i-- {
		ip := hostOnly(strings.TrimSpace(ips[i]))
		if ip == "" {
			continue
		}
		if !ipTrusted(ip, trusted) {
			return ip
		}
	}
	for _, raw := range ips {
		if ip := hostOnly(strings.TrimSpace(raw)); ip != "" {
			return ip
		}
	}
	return remote
}

func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

func ipTrusted(ip string, nets []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}
