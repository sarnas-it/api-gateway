// Package discovery реализует service discovery для api-gateway: находит
// контейнеры Docker/Podman по labels и превращает их в targets/rules.
package discovery

import (
	"context"

	"code.sarnas.ru/api-gateway/internal/config"
)

// Result — результат одного прохода discovery.
type Result struct {
	Targets []config.TargetConfig
	Rules   []config.RoutingRule
}

// Provider — источник состояния сервисов.
type Provider interface {
	// Start запускает отслеживание и вызывает onResult при каждом изменении.
	Start(ctx context.Context, onResult func(Result)) error
	// Stop останавливает отслеживание.
	Stop() error
}

// Container — минимальное представление контейнера из Docker/Podman API.
type Container struct {
	ID       string             `json:"Id"`
	Names    []string           `json:"Names"`
	Labels   map[string]string  `json:"Labels"`
	Ports    []Port             `json:"Ports"`
	Networks map[string]Network `json:"-"`
}

// Port — порт контейнера.
type Port struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Network — сеть контейнера.
type Network struct {
	IPAddress string   `json:"IPAddress"`
	Aliases   []string `json:"Aliases"`
}
