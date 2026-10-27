package permissions

import (
	"net/http"
	"strconv"
	"strings"

	"code.sarnas.ru/api-gateway/internal/config"
	"go.uber.org/zap"
)

type Manager struct {
	client *Client
	cache  *Cache
	logger *zap.Logger
	cfg    *config.PermissionsConfig
}

func NewManager(cfg *config.PermissionsConfig, logger *zap.Logger) *Manager {
	return &Manager{
		client: NewClient(cfg.ServiceURL, cfg.APIKey, cfg.APIKeyHeader, cfg.Method, cfg.Path),
		cache:  NewCache(cfg.CacheTTL),
		logger: logger,
		cfg:    cfg,
	}
}

func (m *Manager) GetPermissions(userID int) ([]string, error) {
	if cached, ok := m.cache.Get(userID); ok {
		return cached.Permissions, nil
	}

	perms, err := m.client.GetEffectivePermissions(userID)
	if err != nil {
		return nil, err
	}

	m.cache.Set(userID, perms)
	return perms.Permissions, nil
}

func (m *Manager) SetHeader(r *http.Request, userID int) error {
	perms, err := m.GetPermissions(userID)
	if err != nil {
		return err
	}

	if len(perms) > 0 {
		r.Header.Set(m.cfg.HeaderName, strings.Join(perms, ","))
	}

	return nil
}

func (m *Manager) InvalidateCacheHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("X-Invalidate-Token")
	if token == "" {
		http.Error(w, `{"error":"missing X-Invalidate-Token"}`, http.StatusUnauthorized)
		return
	}
	if token != m.cfg.InvalidateToken {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}

	userIDStr := r.URL.Query().Get("user_id")
	if userIDStr != "" {
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			http.Error(w, `{"error":"invalid user_id"}`, http.StatusBadRequest)
			return
		}
		m.cache.Invalidate(userID)
		m.logger.Info("invalidated permissions cache", zap.Int("user_id", userID))
	} else {
		m.cache.InvalidateAll()
		m.logger.Info("invalidated all permissions cache")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
