package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
	"github.com/basili4-1982/api-gateway/internal/features/eventsv1"
	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
)

// AuthVerifier — единый интерфейс валидации JWT: реализуется и клиентом, и
// сервером (SO direct), поэтому host не различает транспорт.
type AuthVerifier interface {
	Validate(context.Context, *authv1.ValidateRequest) (*authv1.ValidateResponse, error)
}

// authClientVerifier адаптирует сгенерированный gRPC-клиент к AuthVerifier:
// у клиента метод Validate принимает variadic grpc.CallOption, поэтому он не
// удовлетворяет интерфейсу напрямую. SO-ветка адаптера не требует —
// DirectService возвращает серверную реализацию с точной сигнатурой.
type authClientVerifier struct {
	authv1.AuthServiceClient
}

func (a authClientVerifier) Validate(ctx context.Context, req *authv1.ValidateRequest) (*authv1.ValidateResponse, error) {
	return a.AuthServiceClient.Validate(ctx, req)
}

// Backend — все запущенные плагины и их клиенты.
type Backend struct {
	Auth      AuthVerifier
	RateLimit ratelimitv1.RateLimitServiceClient
	Events    eventsv1.EventServiceClient
	Discovery discoveryv1.DiscoveryServiceClient

	handles []*pluginrpc.Handle
}

// Start запускает сконфигурированные плагины. Возвращает nil, если
// плагины выключены или нет ни одной сконфигурированной фичи.
func Start(ctx context.Context, cfg *config.Config, log *zap.Logger) (*Backend, error) {
	if cfg == nil || !cfg.Plugins.Enabled {
		return nil, nil
	}
	b := &Backend{}
	plog := slog.New(slog.NewTextHandler(os.Stderr, nil))

	start := func(name string, spec *config.PluginSpec, pluginCfg any) (*pluginrpc.Handle, error) {
		if spec == nil {
			return nil, nil
		}
		transport, err := parseTransport(spec.Transport)
		if err != nil {
			return nil, fmt.Errorf("plugins.%s: %w", name, err)
		}
		if transport == pluginrpc.TransportSO && pluginCfg != nil {
			// SO не доставляет PluginConfig: единственный канал — env до open.
			data, err := json.Marshal(pluginCfg)
			if err != nil {
				return nil, fmt.Errorf("plugins.%s: marshal config: %w", name, err)
			}
			if err := os.Setenv(pluginrpc.EnvConfig, string(data)); err != nil {
				return nil, fmt.Errorf("plugins.%s: setenv: %w", name, err)
			}
		}
		h, err := pluginrpc.Start(ctx, pluginrpc.Config{
			Path:         spec.Path,
			Name:         name,
			Transport:    transport,
			PluginConfig: pluginCfg,
			StartTimeout: 10 * time.Second,
			StopTimeout:  3 * time.Second,
			Logger:       plog,
		})
		if err != nil {
			return nil, fmt.Errorf("plugins.%s: start: %w", name, err)
		}
		return h, nil
	}

	if spec := cfg.Plugins.JWT; spec != nil {
		h, err := start("jwt", spec, cfg.JWT)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		if h.Info().Transport == "so" {
			direct, err := pluginrpc.DirectService[authv1.AuthServiceServer](h, "AuthService")
			if err != nil {
				return nil, fmt.Errorf("plugins.jwt: direct: %w", err)
			}
			b.Auth = direct
		} else {
			b.Auth = authClientVerifier{authv1.NewAuthServiceClient(h.Conn())}
		}
	}
	if spec := cfg.Plugins.RateLimit; spec != nil {
		h, err := start("ratelimit", spec, nil)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.RateLimit = ratelimitv1.NewRateLimitServiceClient(h.Conn())
	}
	if spec := cfg.Plugins.Webhooks; spec != nil {
		h, err := start("webhooks", spec, cfg.Webhooks)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.Events = eventsv1.NewEventServiceClient(h.Conn())
	}
	if spec := cfg.Plugins.Discovery; spec != nil {
		h, err := start("discovery", spec, cfg.Discovery)
		if err != nil {
			return nil, err
		}
		b.handles = append(b.handles, h)
		b.Discovery = discoveryv1.NewDiscoveryServiceClient(h.Conn())
	}
	return b, nil
}

func (b *Backend) Stop(ctx context.Context) {
	if b == nil {
		return
	}
	for _, h := range b.handles {
		_ = h.Stop(ctx)
	}
}

func parseTransport(s string) (pluginrpc.Transport, error) {
	switch s {
	case "so":
		return pluginrpc.TransportSO, nil
	case "shared":
		return pluginrpc.TransportShared, nil
	case "fast":
		return pluginrpc.TransportFast, nil
	case "grpc", "":
		return pluginrpc.TransportGRPC, nil
	default:
		return 0, fmt.Errorf("unknown transport %q", s)
	}
}
