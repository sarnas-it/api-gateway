package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"code.sarnas.ru/api-gateway/internal/config"
)

// letsEncryptStagingDirectory — ACME directory тестового CA Let's Encrypt.
const letsEncryptStagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

// workerPathPrefix — протокол воркеров. На публичном листенере он закрыт, но
// админские ручки того же префикса (approve/drain) остаются доступны: иначе
// одобрение слота невозможно без клиентского сертификата.
const workerPathPrefix = "/api/v1/workers/"

// buildCertManager собирает autocert.Manager по TLS-конфигу. При staging
// используется ACME staging directory и отдельный cache-подкаталог, чтобы
// staging-сертификаты не смешивались с production. tls.directory_url
// перекрывает выбор CA.
func buildCertManager(tls *config.TLSConfig) *autocert.Manager {
	cacheDir := tls.CacheDir
	if tls.Staging {
		cacheDir = filepath.Join(cacheDir, "staging")
	}

	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(exactDomains(tls.Domains)...),
		Cache:      autocert.DirCache(cacheDir),
		Email:      tls.Email,
	}

	directoryURL := tls.DirectoryURL
	if directoryURL == "" && tls.Staging {
		directoryURL = letsEncryptStagingDirectory
	}
	if directoryURL != "" {
		manager.Client = &acme.Client{DirectoryURL: directoryURL}
	}

	return manager
}

// exactDomains отбрасывает wildcard-шаблоны: их обслуживает certmagic через
// DNS-01, autocert такие имена выпускать не умеет.
func exactDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if !strings.Contains(d, "*") {
			out = append(out, d)
		}
	}
	return out
}

// metricsOverHTTPHandler отдаёт /metrics напрямую (мидлварь внутри main
// проверяет metrics_allowed_ips), а остальные запросы отправляет на редирект
// HTTP→HTTPS. Нужно, чтобы дашборд мог скрейпить метрики по внутренней сети
// без TLS (autocert требует SNI и не работает по IP).
func metricsOverHTTPHandler(main, redirect http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			main.ServeHTTP(w, r)
			return
		}
		redirect.ServeHTTP(w, r)
	})
}

// isWorkerPath сообщает, относится ли путь к протоколу воркеров. Путь
// нормализуется, чтобы /api/v1/workers/../auth не считался разрешённым.
func isWorkerPath(p string) bool {
	segs := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	if len(segs) < 3 || segs[0] != "api" || segs[1] != "v1" {
		return false
	}
	// Lease-scoped секреты: токен аренды, не credential воркера.
	if segs[2] == "worker-leases" {
		return true
	}
	if segs[2] != "workers" {
		return false
	}
	switch {
	case len(segs) == 4 && segs[3] == "register":
		return true
	case len(segs) == 5 && segs[4] == "heartbeat":
		return true
	case len(segs) >= 6 && segs[4] == "leases":
		return true
	}
	// approve и drain — админские ручки, они остаются на публичном :443.
	return false
}

// workerOnlyHandler пропускает только /api/v1/workers/*, остальное отдаёт 404.
// Листенер с клиентскими сертификатами не должен становиться вторым входом к
// API: mTLS подтверждает хост, но не заменяет маршрутизацию по путям.
func workerOnlyHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWorkerPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicWorkerMutedHandler закрывает протокол воркеров на публичном
// листенере, когда поднят отдельный mTLS-листенер. Иначе mTLS остаётся
// необязательным: воркер мог бы прийти на :443 и обойти проверку сертификата.
func publicWorkerMutedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWorkerPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// buildWorkerTLSConfig собирает TLS-конфиг отдельного листенера воркеров:
// тот же серверный сертификат, но обязательная проверка клиентского
// сертификата по указанному CA.
func buildWorkerTLSConfig(base *tls.Config, clientCAs *x509.CertPool) *tls.Config {
	tlsConfig := base.Clone()
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	tlsConfig.ClientCAs = clientCAs
	tlsConfig.MinVersion = tls.VersionTLS12
	return tlsConfig
}

func (mp *MultiProxy) startTLS() error {
	tlsCfg := mp.config.Load().TLS

	certManager := buildCertManager(tlsCfg)
	autoTLS := certManager.TLSConfig()

	wildcardMagic, stopWildcard, wildcardPatterns, err := buildWildcardCertmagic(tlsCfg, mp.logger)
	if err != nil {
		return fmt.Errorf("wildcard certificates: %w", err)
	}
	if stopWildcard != nil {
		mp.certmagicStop = stopWildcard
	}
	var wildcardTLS *tls.Config
	if wildcardMagic != nil {
		wildcardTLS = wildcardMagic.TLSConfig()
	}
	serverTLS := combineTLSConfig(autoTLS, wildcardTLS, wildcardPatterns)

	// CA грузим до старта любого листенера: ошибка конфигурации mTLS должна
	// ронять процесс, а не оставлять воркеров без аутентификации.
	var workerClientCAs *x509.CertPool
	if tlsCfg.WorkerPort > 0 {
		pool, err := config.LoadClientCAPool(tlsCfg.WorkerClientCAFile)
		if err != nil {
			return fmt.Errorf("worker mTLS listener: %w", err)
		}
		workerClientCAs = pool
	}

	publicHandler := http.Handler(mp)
	if tlsCfg.WorkerPort > 0 {
		publicHandler = publicWorkerMutedHandler(publicHandler)
	}

	tlsServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", tlsCfg.Port),
		Handler:      publicHandler,
		ReadTimeout:  mp.config.Load().Server.ReadTimeout,
		WriteTimeout: mp.config.Load().Server.WriteTimeout,
		IdleTimeout:  mp.config.Load().Server.IdleTimeout,
		TLSConfig:    serverTLS,
	}
	mp.httpsServer = tlsServer

	httpHandler := certManager.HTTPHandler(metricsOverHTTPHandler(publicHandler, mp.httpRedirectHandler()))
	httpServer := &http.Server{
		Addr:        fmt.Sprintf(":%d", tlsCfg.HTTPPort),
		Handler:     httpHandler,
		ReadTimeout: mp.config.Load().Server.ReadTimeout,
		IdleTimeout: mp.config.Load().Server.IdleTimeout,
	}
	mp.httpServer = httpServer

	var workerServer *http.Server
	if tlsCfg.WorkerPort > 0 {
		workerServer = &http.Server{
			Addr:         fmt.Sprintf(":%d", tlsCfg.WorkerPort),
			Handler:      workerOnlyHandler(mp),
			ReadTimeout:  mp.config.Load().Server.ReadTimeout,
			WriteTimeout: mp.config.Load().Server.WriteTimeout,
			IdleTimeout:  mp.config.Load().Server.IdleTimeout,
			TLSConfig:    buildWorkerTLSConfig(serverTLS, workerClientCAs),
		}
		mp.workerServer = workerServer
	}

	mp.logger.Info("Starting HTTPS proxy server with auto TLS",
		zap.Int("https_port", tlsCfg.Port),
		zap.Int("http_port", tlsCfg.HTTPPort),
		zap.Strings("domains", tlsCfg.Domains),
		zap.Strings("wildcard_domains", wildcardPatterns),
		zap.String("email", tlsCfg.Email),
		zap.String("cache_dir", tlsCfg.CacheDir),
		zap.Bool("staging", tlsCfg.Staging),
		zap.String("directory_url", tlsCfg.DirectoryURL),
		zap.Int("targets", len(mp.targets)),
		zap.Bool("worker_paths_on_public_listener", tlsCfg.WorkerPort == 0),
	)

	mp.logTargets()

	errCh := make(chan error, 3)

	go func() {
		mp.logger.Info("Starting HTTP server (ACME challenge + redirect)",
			zap.Int("port", tlsCfg.HTTPPort),
		)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	go func() {
		mp.logger.Info("Starting HTTPS server",
			zap.Int("port", tlsCfg.Port),
		)
		if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTPS server error: %w", err)
		}
	}()

	if workerServer != nil {
		go func() {
			// Не логируем содержимое CA и ключей — только путь к файлу.
			mp.logger.Info("Starting worker mTLS server",
				zap.Int("port", tlsCfg.WorkerPort),
				zap.String("client_ca_file", tlsCfg.WorkerClientCAFile),
				zap.String("client_auth", "require_and_verify"),
				zap.String("path_prefix", workerPathPrefix),
			)
			if err := workerServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("worker mTLS server error: %w", err)
			}
		}()
	}

	return <-errCh
}
