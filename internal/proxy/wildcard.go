package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"
	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

const regruAPIBase = "https://api.reg.ru/api/regru2"

// shortcut: DNS-01 только через reg.ru; другой провайдер потребует libdns-адаптер; добавить, если домены уйдут с reg.ru.
// regruDNS — минимальный libdns-провайдер для reg.ru: TXT-записи для
// ACME DNS-01. Отдельный клиент, чтобы не тянуть приватный regru-go в CI.
type regruDNS struct {
	baseURL  string
	username string
	password string
	client   *http.Client
}

func newRegruDNS(username, password string) *regruDNS {
	return &regruDNS{
		baseURL:  regruAPIBase,
		username: username,
		password: password,
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (r *regruDNS) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	for _, rec := range recs {
		rr := rec.RR()
		if !strings.EqualFold(rr.Type, "TXT") {
			continue
		}
		if err := r.call(ctx, "zone/add_txt", map[string]any{
			"domains":   []map[string]string{{"dname": strings.TrimSuffix(zone, ".")}},
			"subdomain": strings.TrimSuffix(rr.Name, "."),
			"text":      rr.Data,
		}); err != nil {
			return nil, err
		}
	}
	return recs, nil
}

func (r *regruDNS) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	for _, rec := range recs {
		rr := rec.RR()
		if !strings.EqualFold(rr.Type, "TXT") {
			continue
		}
		if err := r.call(ctx, "zone/remove_record", map[string]any{
			"domains":     []map[string]string{{"dname": strings.TrimSuffix(zone, ".")}},
			"subdomain":   strings.TrimSuffix(rr.Name, "."),
			"record_type": "TXT",
			"content":     rr.Data,
		}); err != nil {
			return nil, err
		}
	}
	return recs, nil
}

func (r *regruDNS) call(ctx context.Context, method string, input any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("regru: encode %s: %w", method, err)
	}
	form := url.Values{}
	form.Set("username", r.username)
	form.Set("password", r.password)
	form.Set("input_format", "json")
	form.Set("input_data", string(data))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("regru: %s: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("regru: %s: read: %w", method, err)
	}
	var env struct {
		Result    string `json:"result"`
		ErrorCode string `json:"error_code"`
		ErrorText string `json:"error_text"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("regru: %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if env.Result != "success" {
		return fmt.Errorf("regru: %s: %s %s", method, env.ErrorCode, env.ErrorText)
	}
	return nil
}

// wildcardDomains возвращает домены с "*", нормализуя регистр.
func wildcardDomains(domains []string) []string {
	out := make([]string, 0)
	for _, d := range domains {
		if strings.Contains(d, "*") {
			out = append(out, strings.ToLower(strings.TrimSuffix(d, ".")))
		}
	}
	return out
}

// hostMatchesWildcard проверяет SNI против шаблонов *.example.com. TLS-wildcard
// покрывает ровно один уровень, поэтому a.b.example.com сюда не попадает.
func hostMatchesWildcard(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
		if !strings.HasPrefix(pattern, "*.") {
			continue
		}
		suffix := pattern[1:] // ".example.com"
		if !strings.HasSuffix(host, suffix) {
			continue
		}
		label := strings.TrimSuffix(host, suffix)
		if label != "" && !strings.Contains(label, ".") {
			return true
		}
	}
	return false
}

// buildWildcardCertmagic создаёт certmagic с DNS-01 для wildcard-доменов.
// Возвращает nil, если wildcard-доменов нет.
func buildWildcardCertmagic(tlsCfg *config.TLSConfig, logger *zap.Logger) (*certmagic.Config, func(), []string, error) {
	wildcards := wildcardDomains(tlsCfg.Domains)
	if len(wildcards) == 0 {
		return nil, nil, nil, nil
	}

	provider := newRegruDNS(tlsCfg.DNSUsername, tlsCfg.DNSPassword)
	storage := &certmagic.FileStorage{Path: tlsCfg.CacheDir}
	var magicPtr atomic.Pointer[certmagic.Config]
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			if magic := magicPtr.Load(); magic != nil {
				return magic, nil
			}
			return nil, fmt.Errorf("certmagic is not initialized")
		},
		Logger: logger.Named("certmagic"),
	})

	ca := tlsCfg.DirectoryURL
	if ca == "" {
		if tlsCfg.Staging {
			ca = certmagic.LetsEncryptStagingCA
		} else {
			ca = certmagic.LetsEncryptProductionCA
		}
	}
	magic := certmagic.New(cache, certmagic.Config{
		Storage: storage,
		Logger:  logger.Named("certmagic"),
		Issuers: []certmagic.Issuer{&certmagic.ACMEIssuer{
			CA:                      ca,
			Email:                   tlsCfg.Email,
			Agreed:                  true,
			DisableHTTPChallenge:    true,
			DisableTLSALPNChallenge: true,
			DNS01Solver: &certmagic.DNS01Solver{
				DNSManager: certmagic.DNSManager{
					DNSProvider:        provider,
					TTL:                60 * time.Second,
					PropagationTimeout: tlsCfg.DNSPropagationTimeout,
					Logger:             logger.Named("certmagic-dns"),
				},
			},
		}},
	})
	magicPtr.Store(magic)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := magic.ManageSync(ctx, wildcards); err != nil {
		cache.Stop()
		return nil, nil, nil, fmt.Errorf("wildcard certificates: %w", err)
	}
	return magic, cache.Stop, wildcards, nil
}

// combineTLSConfig оставляет autocert для точных доменов и certmagic — для
// wildcard. NextProtos объединяются, чтобы сохранить ACME TLS-ALPN и h2.
func combineTLSConfig(autoCfg, wildcardCfg *tls.Config, wildcards []string) *tls.Config {
	if wildcardCfg == nil {
		return autoCfg
	}
	combined := autoCfg.Clone()
	autoGet := autoCfg.GetCertificate
	wildcardGet := wildcardCfg.GetCertificate
	combined.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hostMatchesWildcard(hello.ServerName, wildcards) {
			return wildcardGet(hello)
		}
		return autoGet(hello)
	}
	for _, proto := range wildcardCfg.NextProtos {
		if !containsString(combined.NextProtos, proto) {
			combined.NextProtos = append(combined.NextProtos, proto)
		}
	}
	return combined
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
