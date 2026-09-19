package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/discovery"
	"github.com/basili4-1982/api-gateway/internal/features/discoveryv1"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type discoveryServer struct {
	discoveryv1.UnimplementedDiscoveryServiceServer
	mu   sync.Mutex
	dcfg *config.DiscoveryConfig
}

func (s *discoveryServer) Watch(_ *discoveryv1.WatchRequest, stream grpc.ServerStreamingServer[discoveryv1.DiscoveryResult]) error {
	log, _ := zap.NewProduction()
	provider, err := discovery.NewDockerProvider(
		s.dcfg.Host, s.dcfg.APIVersion,
		discovery.ParseOptions{
			LabelPrefix:       s.dcfg.LabelPrefix,
			ServiceNameLabels: s.dcfg.ServiceNameLabels,
			DefaultTimeout:    s.dcfg.DefaultTimeout,
			Network:           s.dcfg.Network,
		},
		s.dcfg.Debounce, s.dcfg.ResyncInterval, log,
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	defer provider.Stop()
	return provider.Start(ctx, func(r discovery.Result) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if serr := stream.Send(discoveryResultToProto(r)); serr != nil {
			cancel()
		}
	})
}

func discoveryResultToProto(r discovery.Result) *discoveryv1.DiscoveryResult {
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

func main() {
	dcfg, err := pluginrpc.PluginConfig[config.DiscoveryConfig]()
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery plugin:", err)
		os.Exit(1)
	}
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "discovery",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			discoveryv1.RegisterDiscoveryServiceServer(s, &discoveryServer{dcfg: &dcfg})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "discovery plugin:", err)
		os.Exit(1)
	}
}
