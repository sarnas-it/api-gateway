package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/basili4-1982/api-gateway/internal/features/ratelimitv1"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

type rateServer struct {
	ratelimitv1.UnimplementedRateLimitServiceServer
	mu       sync.Mutex
	limiters map[string]*proxy.IPRateLimiter
}

func (s *rateServer) Allow(_ context.Context, req *ratelimitv1.AllowRequest) (*ratelimitv1.AllowResponse, error) {
	s.mu.Lock()
	l := s.limiters[req.RouteKey]
	if l == nil {
		l = proxy.NewIPRateLimiter(req.Rate, int(req.Burst))
		s.limiters[req.RouteKey] = l
	}
	s.mu.Unlock()
	return &ratelimitv1.AllowResponse{Allowed: l.Allow(req.Ip)}, nil
}

func main() {
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "ratelimit",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			ratelimitv1.RegisterRateLimitServiceServer(s, &rateServer{limiters: make(map[string]*proxy.IPRateLimiter)})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ratelimit plugin:", err)
		os.Exit(1)
	}
}
