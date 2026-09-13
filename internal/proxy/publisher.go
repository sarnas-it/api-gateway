package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// ResponseBody — тело ответа (только JSON, с ограничением размера).
	// Публикуется, если у вебхука включён include_response_body.
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
}

// defaultMaxResponseBodyBytes — потолок захвата тела ответа для публикации.
const defaultMaxResponseBodyBytes = 64 << 10 // 64 KiB

type Publisher struct {
	nc       *nats.Conn
	webhooks []config.WebhookConfig
	log      *zap.Logger
	client   *http.Client
	batchers map[string]*webhookBatcher
}

func NewPublisher(cfg *config.Config, log *zap.Logger) (*Publisher, error) {
	p := &Publisher{
		log:      log,
		webhooks: cfg.Webhooks,
		batchers: make(map[string]*webhookBatcher),
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}

	// Батчинг HTTP-вебхуков: события копятся и уходят одним POST.
	for _, wh := range cfg.Webhooks {
		if wh.Transport == config.TransportWebhook && wh.BatchSize > 1 {
			p.batchers[wh.Name] = newWebhookBatcher(wh, p.client, log)
			log.Info("webhook batching enabled",
				zap.String("webhook", wh.Name),
				zap.Int("batch_size", wh.BatchSize),
				zap.Duration("flush_interval", wh.FlushInterval),
			)
		}
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
	for _, b := range p.batchers {
		b.close()
	}
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

func (p *Publisher) buildEvent(r *http.Request, statusCode int, wh config.WebhookConfig) AuditEvent {
	reqID, _ := requestIDsFromContext(r.Context())
	e := AuditEvent{
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		RequestID:  reqID,
		Timestamp:  time.Now(),
		StatusCode: statusCode,
		Headers:    make(map[string]string),
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

	if wh.IncludeRequestBodyEnabled() {
		if bodyBytes, ok := r.Context().Value(ctxKeyRequestBody).([]byte); ok && len(bodyBytes) > 0 {
			var parsed map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil && len(parsed) > 0 {
				raw, _ := json.Marshal(parsed)
				e.Changes = raw
			}
		}
	}

	if wh.IncludeResponseBody {
		if body, ok := r.Context().Value(ctxKeyResponseBody).([]byte); ok && len(body) > 0 {
			if ct, _ := r.Context().Value(ctxKeyResponseContentType).(string); isJSONContentType(ct) {
				e.ResponseBody = body
			}
		}
	}

	return e
}

// isJSONContentType — тело ответа публикуется только для JSON (не тащим
// бинарные/файловые ответы в события).
func isJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return strings.Contains(ct, "application/json") || strings.HasSuffix(ct, "+json")
}

// CaptureResponseBodies — нужно ли собирать тело ответа для запросов
// (хотя бы один on_response вебхук с include_response_body).
func (p *Publisher) CaptureResponseBodies() bool {
	if p == nil {
		return false
	}
	for _, wh := range p.webhooks {
		if wh.Trigger == config.TriggerOnResponse && wh.IncludeResponseBody {
			return true
		}
	}
	return false
}

func (p *Publisher) PublishOnRequest(ctx context.Context, r *http.Request) {
	for _, wh := range p.webhooks {
		if wh.Trigger != config.TriggerOnRequest {
			continue
		}
		if !p.shouldPublish(wh, r, 0) {
			continue
		}

		event := p.buildEvent(r, 0, wh)
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

		event := p.buildEvent(r, statusCode, wh)
		p.publish(ctx, wh, event)
	}
}

func (p *Publisher) publish(ctx context.Context, wh config.WebhookConfig, event AuditEvent) {
	// Батчинг: событие уходит в очередь вебхука, отправку пачкой делает воркер.
	if b := p.batchers[wh.Name]; b != nil {
		b.enqueue(event)
		return
	}

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
	// Вебхук не должен зависеть от времени жизни запроса: on_response срабатывает
	// после ответа, а async — после возврата хендлера, когда request-context уже
	// отменён. Отвязываем контекст (WithoutCancel) и ограничиваем отправку своим
	// таймаутом, иначе POST отменяется и событие не доставляется.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.WebhookURL, bytes.NewReader(data))
	if err != nil {
		p.log.Error("failed to create webhook request", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := p.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		p.log.Error("failed to send webhook", zap.Error(err), zap.String("url", wh.WebhookURL))
		return
	}
	resp.Body.Close()
}
