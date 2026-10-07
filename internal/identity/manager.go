package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
)

type provider struct {
	name       string
	validator  *jwtutil.JWTValidator
	emailClaim string
}

// Manager перебирает доверенные внешние провайдеры и резолвит нашего пользователя.
type Manager struct {
	providers []provider
	resolver  *resolver
	logger    *zap.Logger
}

func NewManager(cfg config.IdentityConfig, logger *zap.Logger) (*Manager, error) {
	var providers []provider

	for _, pc := range cfg.Providers {
		if !pc.Enabled {
			continue
		}
		v, err := jwtutil.NewJWTValidator(
			pc.SecretKey, pc.Algorithm,
			pc.ValidateExp, pc.ValidateIss, pc.ExpectedIss,
			false, "", pc.PublicKeyFile,
		)
		if err != nil {
			return nil, fmt.Errorf("identity provider %s: %w", pc.Name, err)
		}
		providers = append(providers, provider{name: pc.Name, validator: v, emailClaim: pc.EmailClaim})
	}

	providers = filterProviders(providers, cfg.Selection)
	if len(providers) == 0 {
		return nil, errors.New("identity: no providers selected")
	}

	return &Manager{
		providers: providers,
		resolver:  newResolver(cfg.UserLookup),
		logger:    logger,
	}, nil
}

// Resolve проверяет токен по выбранным провайдерам и возвращает нашего пользователя.
func (m *Manager) Resolve(ctx context.Context, token string) (*User, error) {
	if m == nil {
		return nil, errors.New("identity: no provider matched")
	}
	for _, p := range m.providers {
		claims, err := p.validator.ParseAndValidate(token)
		if err != nil {
			continue
		}
		if err := p.validator.ValidateClaims(claims); err != nil {
			continue
		}

		email, _ := claims[p.emailClaim].(string)
		email = strings.TrimSpace(email)
		if email == "" {
			continue
		}

		user, found, err := m.resolver.byEmail(ctx, email)
		if err != nil {
			m.logger.Warn("identity: user lookup failed",
				zap.String("provider", p.name), zap.Error(err))
			continue
		}
		if !found || user == nil || !user.IsActive {
			continue
		}

		m.logger.Info("identity: external token resolved",
			zap.String("provider", p.name), zap.Int("user_id", user.ID))
		return user, nil
	}

	return nil, errors.New("identity: no provider matched")
}

func filterProviders(all []provider, selection []string) []provider {
	if len(selection) == 0 {
		return all
	}
	want := make(map[string]bool, len(selection))
	for _, s := range selection {
		want[s] = true
	}
	var out []provider
	for _, p := range all {
		if want[p.name] {
			out = append(out, p)
		}
	}
	return out
}
