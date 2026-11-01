// Package plugin is the compile-time module registry. A module registers itself
// from init(); xgate builds a custom main with blank imports.
package plugin

import "sync"

// Module describes an optional gateway module.
type Module interface {
	Name() string
}

var (
	mu      sync.RWMutex
	modules []Module
)

func Register(m Module) {
	if m == nil || m.Name() == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	modules = append(modules, m)
}

func Modules() []Module {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Module, len(modules))
	copy(out, modules)
	return out
}
