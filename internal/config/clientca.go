package config

import (
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// LoadClientCAPool читает PEM-файл с одним или несколькими CA-сертификатами и
// возвращает пул для проверки клиентских сертификатов mTLS. Пустой путь,
// нечитаемый файл и файл без валидных PEM-сертификатов — ошибка (fail closed).
func LoadClientCAPool(path string) (*x509.CertPool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("client CA file path is empty")
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read client CA file %q: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("client CA file %q contains no valid PEM certificates", path)
	}

	return pool, nil
}
