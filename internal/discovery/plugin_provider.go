package discovery

import (
	"context"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
)

// pluginProvider — Provider поверх pluginrpc-стрима: хост тянет
// DiscoveryResult'ы и отдаёт их через onResult.
type pluginProvider struct {
	client discoveryv1.DiscoveryServiceClient
	cancel context.CancelFunc
}

func NewPluginProvider(client discoveryv1.DiscoveryServiceClient) Provider {
	return &pluginProvider{client: client}
}

func (p *pluginProvider) Start(ctx context.Context, onResult func(Result)) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	defer cancel()

	stream, err := p.client.Watch(ctx, &discoveryv1.WatchRequest{})
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		onResult(resultFromProto(msg))
	}
}

func (p *pluginProvider) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func resultToProto(r Result) *discoveryv1.DiscoveryResult {
	out := &discoveryv1.DiscoveryResult{
		Targets: make([]*discoveryv1.Target, 0, len(r.Targets)),
		Rules:   make([]*discoveryv1.Rule, 0, len(r.Rules)),
	}
	for _, t := range r.Targets {
		wt := int32(0)
		if t.Weight != nil {
			wt = int32(*t.Weight)
		}
		out.Targets = append(out.Targets, &discoveryv1.Target{
			Name: t.Name, Url: t.URL, PathPrefix: t.PathPrefix,
			StripPrefix: t.StripPrefix, Weight: wt, HealthCheck: t.HealthCheck,
		})
	}
	for _, rl := range r.Rules {
		out.Rules = append(out.Rules, &discoveryv1.Rule{
			Host: rl.Host, PathPrefix: rl.PathPrefix, TargetName: rl.TargetName,
			Methods: rl.Methods, StripPath: rl.StripPath,
		})
	}
	return out
}

func resultFromProto(m *discoveryv1.DiscoveryResult) Result {
	out := Result{
		Targets: make([]config.TargetConfig, 0, len(m.Targets)),
		Rules:   make([]config.RoutingRule, 0, len(m.Rules)),
	}
	for _, t := range m.Targets {
		var w *int
		if t.Weight != 0 {
			wi := int(t.Weight)
			w = &wi
		}
		out.Targets = append(out.Targets, config.TargetConfig{
			Name: t.Name, URL: t.Url, PathPrefix: t.PathPrefix,
			StripPrefix: t.StripPrefix, Weight: w, HealthCheck: t.HealthCheck,
		})
	}
	for _, rl := range m.Rules {
		out.Rules = append(out.Rules, config.RoutingRule{
			Host: rl.Host, PathPrefix: rl.PathPrefix, TargetName: rl.TargetName,
			Methods: rl.Methods, StripPath: rl.StripPath,
		})
	}
	return out
}
