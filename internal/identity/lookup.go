package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	hmacauth "github.com/X-didgital/go-hmac-auth"

	"github.com/basili4-1982/api-gateway/internal/config"
)

// User — минимальный профиль нашего пользователя, нужный шлюзу.
type User struct {
	ID       int
	Email    string
	FullName string
	IsActive bool
	Roles    []string
}

type lookupClient struct {
	baseURL string
	secret  string
	http    *http.Client
}

func newLookupClient(baseURL, secret string) *lookupClient {
	return &lookupClient{
		baseURL: baseURL,
		secret:  secret,
		http:    &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *lookupClient) fetch(ctx context.Context, email string) (*User, error) {
	const path = "/api/v1/internal/user-by-email"

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("parse lookup url: %w", err)
	}
	q := u.Query()
	q.Set("email", email)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create lookup request: %w", err)
	}

	ts := time.Now()
	sig, tsStr := hmacauth.SignRequest("gateway", http.MethodGet, path, c.secret, ts)
	req.Header.Set(hmacauth.UserIDHeader, "gateway")
	req.Header.Set(hmacauth.SignatureHeader, sig)
	req.Header.Set(hmacauth.TimestampHeader, tsStr)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lookup request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lookup returned %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			ID       int      `json:"id"`
			Email    string   `json:"email"`
			FullName string   `json:"full_name"`
			IsActive bool     `json:"is_active"`
			Roles    []string `json:"roles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode lookup response: %w", err)
	}

	return &User{
		ID:       body.Data.ID,
		Email:    body.Data.Email,
		FullName: body.Data.FullName,
		IsActive: body.Data.IsActive,
		Roles:    body.Data.Roles,
	}, nil
}

type cacheEntry struct {
	user      *User // nil = негативный результат (не найден)
	expiresAt time.Time
}

type lookupCache struct {
	mu    sync.RWMutex
	store map[string]cacheEntry
	ttl   time.Duration
}

func newLookupCache(ttl time.Duration) *lookupCache {
	return &lookupCache{store: make(map[string]cacheEntry), ttl: ttl}
}

func (c *lookupCache) get(email string) (*User, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.store[email]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.user, true
}

func (c *lookupCache) set(email string, u *User) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[email] = cacheEntry{user: u, expiresAt: time.Now().Add(c.ttl)}
}

type resolver struct {
	client *lookupClient
	cache  *lookupCache
}

func newResolver(cfg config.IdentityUserLookupConfig) *resolver {
	return &resolver{
		client: newLookupClient(cfg.ServiceURL, cfg.HMACSecret),
		cache:  newLookupCache(cfg.CacheTTL),
	}
}

// byEmail возвращает (user, found, error). found=false при 404 или кэш-промахе.
func (r *resolver) byEmail(ctx context.Context, email string) (*User, bool, error) {
	if u, ok := r.cache.get(email); ok {
		if u == nil {
			return nil, false, nil
		}
		return u, true, nil
	}

	u, err := r.client.fetch(ctx, email)
	if err != nil {
		return nil, false, err
	}
	r.cache.set(email, u) // u может быть nil — негативное кэширование
	if u == nil {
		return nil, false, nil
	}
	return u, true, nil
}
