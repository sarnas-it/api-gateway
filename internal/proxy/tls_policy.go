package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"code.sarnas.ru/api-gateway/internal/config"
)

// applyTLSPolicy накладывает min_version/ciphers/curves/client_auth на
// серверный TLS-конфиг (включая wildcard-сертификаты и worker-листенер).
func applyTLSPolicy(cfg *tls.Config, tlsCfg *config.TLSConfig) error {
	minVersion, err := config.ParseTLSVersion(tlsCfg.MinVersion)
	if err != nil {
		return err
	}
	cfg.MinVersion = minVersion

	ciphers, err := config.ParseCipherSuites(tlsCfg.CipherSuites)
	if err != nil {
		return err
	}
	cfg.CipherSuites = ciphers

	curves, err := config.ParseCurves(tlsCfg.Curves)
	if err != nil {
		return err
	}
	cfg.CurvePreferences = curves

	clientAuth, err := config.ParseClientAuth(tlsCfg.ClientAuth)
	if err != nil {
		return err
	}
	cfg.ClientAuth = clientAuth
	if tlsCfg.ClientCAFile != "" {
		pool, err := config.LoadClientCAPool(tlsCfg.ClientCAFile)
		if err != nil {
			return fmt.Errorf("load client CA: %w", err)
		}
		cfg.ClientCAs = pool
	}
	return nil
}

// buildUpstreamTLS собирает TLS-конфиг для HTTPS-бэкенда.
func buildUpstreamTLS(c *config.UpstreamTLSConfig) (*tls.Config, error) {
	if c == nil {
		return nil, nil
	}
	cfg := &tls.Config{
		// Явный opt-in в конфиге; иначе проверка цепочки остаётся включённой.
		InsecureSkipVerify: c.InsecureSkipVerify, //nolint:gosec
		ServerName:         c.ServerName,
		MinVersion:         tls.VersionTLS12,
	}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s contains no certificates", c.CAFile)
		}
		cfg.RootCAs = pool
	}
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
