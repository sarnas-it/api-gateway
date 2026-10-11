package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

type AuditEvent struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query,omitempty"`
	UserID     string            `json:"user_id,omitempty"`
	UserEmail  string            `json:"user_email,omitempty"`
	UserRoles  string            `json:"user_roles,omitempty"`
	RequestID  string            `json:"request_id"`
	StatusCode int               `json:"status_code,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
	Headers    map[string]string `json:"headers,omitempty"`
	Changes    json.RawMessage   `json:"changes,omitempty"`
	// IP и UserAgent — откуда пришёл запрос (аудит).
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	// ResponseBody — тело ответа апстрима (нужно для вывода id созданного
	// объекта на стороне audit-consumer).
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
}

// clientIPFromRequest возвращает клиентский IP: первый валидный адрес из
// X-Forwarded-For, иначе хост из RemoteAddr.
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type Publisher struct {
	nc       *nats.Conn
	webhooks []config.WebhookConfig
	log      *zap.Logger
}

func NewPublisher(cfg *config.Config, log *zap.Logger) (*Publisher, error) {
	p := &Publisher{
		log:      log,
		webhooks: cfg.Webhooks,
	}

	hasNATS := false
	for _, wh := range cfg.Webhooks {
		if wh.Transport == config.TransportNATS {
			hasNATS = true
			break
		}
	}

	if !hasNATS {
		log.Info("no NATS webhooks configured, publisher disabled")
		return p, nil
	}

	nc, err := nats.Connect(
		cfg.Webhooks[0].NATSURL,
		nats.Name("api-gateway"),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("NATS disconnected", zap.Error(err))
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Info("NATS reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	p.nc = nc
	log.Info("connected to NATS", zap.String("url", cfg.Webhooks[0].NATSURL))
	return p, nil
}

func (p *Publisher) Close() {
	if p.nc != nil {
		p.nc.Close()
		p.log.Info("NATS connection closed")
	}
}

func (p *Publisher) shouldPublish(wh config.WebhookConfig, r *http.Request, statusCode int) bool {
	if len(wh.Methods) > 0 {
		methodAllowed := false
		for _, m := range wh.Methods {
			if strings.EqualFold(m, r.Method) {
				methodAllowed = true
				break
			}
		}
		if !methodAllowed {
			return false
		}
	}

	for _, prefix := range wh.ExcludePaths {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return false
		}
	}

	if wh.Trigger == config.TriggerOnResponse && len(wh.OnStatusCodes) > 0 {
		statusAllowed := false
		for _, code := range wh.OnStatusCodes {
			if statusCode == code {
				statusAllowed = true
				break
			}
		}
		if !statusAllowed {
			return false
		}
	}

	return true
}

func (p *Publisher) buildEvent(r *http.Request, statusCode int) AuditEvent {
	e := AuditEvent{
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		RequestID:  r.Context().Value(ctxKeyRequestID).(string),
		Timestamp:  time.Now(),
		StatusCode: statusCode,
		Headers:    make(map[string]string),
		IP:         clientIPFromRequest(r),
		UserAgent:  r.UserAgent(),
	}

	if rc, ok := r.Context().Value(ctxKeyResponseBody).(*responseCapture); ok && len(rc.body) > 0 {
		e.ResponseBody = json.RawMessage(rc.body)
	}

	if id := r.Header.Get("X-User-ID"); id != "" {
		e.UserID = id
	}
	if email := r.Header.Get("X-User-Email"); email != "" {
		e.UserEmail = email
	}
	if roles := r.Header.Get("X-User-Roles"); roles != "" {
		e.UserRoles = roles
	}

	if bodyBytes, ok := r.Context().Value(ctxKeyRequestBody).([]byte); ok && len(bodyBytes) > 0 {
		var parsed map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &parsed); err == nil && len(parsed) > 0 {
			raw, _ := json.Marshal(parsed)
			e.Changes = raw
		}
	}

	return e
}

func (p *Publisher) PublishOnRequest(ctx context.Context, r *http.Request) {
	for _, wh := range p.webhooks {
		if wh.Trigger != config.TriggerOnRequest {
			continue
		}
		if !p.shouldPublish(wh, r, 0) {
			continue
		}

		event := p.buildEvent(r, 0)
		p.publish(ctx, wh, event)
	}
}

func (p *Publisher) PublishOnResponse(ctx context.Context, r *http.Request, statusCode int) {
	for _, wh := range p.webhooks {
		if wh.Trigger != config.TriggerOnResponse {
			continue
		}
		if !p.shouldPublish(wh, r, statusCode) {
			continue
		}

		event := p.buildEvent(r, statusCode)
		p.publish(ctx, wh, event)
	}
}

func (p *Publisher) publish(ctx context.Context, wh config.WebhookConfig, event AuditEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		p.log.Error("failed to marshal audit event", zap.Error(err))
		return
	}

	switch wh.Transport {
	case config.TransportNATS:
		if p.nc == nil {
			p.log.Warn("NATS not connected, skipping publish")
			return
		}
		if wh.Async {
			go func() {
				if err := p.nc.Publish(wh.Subject, data); err != nil {
					p.log.Error("failed to publish NATS message", zap.Error(err), zap.String("subject", wh.Subject))
				}
			}()
		} else {
			if err := p.nc.Publish(wh.Subject, data); err != nil {
				p.log.Error("failed to publish NATS message", zap.Error(err), zap.String("subject", wh.Subject))
			}
		}
		p.log.Debug("published NATS event",
			zap.String("subject", wh.Subject),
			zap.String("method", event.Method),
			zap.String("path", event.Path),
		)

	case config.TransportWebhook:
		if wh.Async {
			go p.doWebhook(ctx, wh, data)
		} else {
			p.doWebhook(ctx, wh, data)
		}
	}
}

func (p *Publisher) doWebhook(ctx context.Context, wh config.WebhookConfig, data []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.WebhookURL, nil)
	if err != nil {
		p.log.Error("failed to create webhook request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.log.Error("failed to send webhook", zap.Error(err), zap.String("url", wh.WebhookURL))
		return
	}
	resp.Body.Close()
}
