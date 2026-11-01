package plugin

import "context"

// DiscoveredService — один инстанс сервиса, найденный модулем discovery.
type DiscoveredService struct {
	// Name — имя таргета, уникальное в пределах провайдера.
	Name string
	// Address — адрес инстанса: "host:port" или полный URL со схемой.
	Address string
	// PathPrefix — префикс пути для маршрута; пусто — "/"+Name.
	PathPrefix string
	// StripPath — удалять префикс при проксировании.
	StripPath bool
}

// DiscoveryProvider — модуль-источник сервисов для `discovery.module`.
//
// Watch вызывает emit при каждом изменении набора сервисов и блокируется до
// отмены ctx (или до фатальной ошибки — тогда discovery останавливается с
// предупреждением в логе). Вызывать emit нужно только при реальном изменении:
// каждое событие пересобирает конфиг прокси.
type DiscoveryProvider interface {
	Module
	Watch(ctx context.Context, emit func([]DiscoveredService)) error
}

// DiscoveryProviders возвращает скомпилированные discovery-провайдеры по имени модуля.
func DiscoveryProviders() map[string]DiscoveryProvider {
	mods := Modules()
	out := make(map[string]DiscoveryProvider)
	for _, m := range mods {
		if p, ok := m.(DiscoveryProvider); ok {
			out[m.Name()] = p
		}
	}
	return out
}
