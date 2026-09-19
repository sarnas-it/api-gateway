package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/eventsv1"
	"github.com/basili4-1982/api-gateway/internal/proxy"
	"github.com/sarnas-it/pluginrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type eventsServer struct {
	eventsv1.UnimplementedEventServiceServer
	pub *proxy.Publisher
}

func (s *eventsServer) Publish(ctx context.Context, req *eventsv1.PublishRequest) (*eventsv1.PublishResponse, error) {
	s.pub.Deliver(ctx, req.WebhookName, toAuditEvent(req.Event))
	return &eventsv1.PublishResponse{}, nil
}

func toAuditEvent(e *eventsv1.AuditEvent) proxy.AuditEvent {
	ev := proxy.AuditEvent{
		Method: e.Method, Path: e.Path, Query: e.Query,
		UserID: e.UserId, UserEmail: e.UserEmail, UserRoles: e.UserRoles,
		RequestID: e.RequestId, StatusCode: int(e.StatusCode),
		Timestamp: time.Unix(0, e.TimestampNanos),
		Headers:   e.Headers,
	}
	if len(e.Changes) > 0 {
		ev.Changes = json.RawMessage(e.Changes)
	}
	if len(e.ResponseBody) > 0 {
		ev.ResponseBody = json.RawMessage(e.ResponseBody)
	}
	return ev
}

func main() {
	whs, err := pluginrpc.PluginConfig[[]config.WebhookConfig]()
	if err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
	log, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pub, err := proxy.NewPublisher(&config.Config{Webhooks: whs}, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "webhooks",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			eventsv1.RegisterEventServiceServer(s, &eventsServer{pub: pub})
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "events plugin:", err)
		os.Exit(1)
	}
}
