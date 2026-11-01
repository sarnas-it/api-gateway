package plugin

import "net/http"

// AuthConnector — модуль, который аутентифицирует запрос способом, отличным
// от встроенного JWT (API-ключ, introspection, mTLS-идентичность).
//
// Маршрут выбирает коннектор через `auth.connector: <name>`. Выбранный
// коннектор — единственный аутентификатор маршрута: JWT при этом не
// проверяется, а отсутствие identity или ошибка коннектора означают 401.
type AuthConnector interface {
	Module
	// Authenticate возвращает claims запроса. Пустой результат (nil) —
	// «identity нет», запрос будет отклонён. Обязательный claim — "id"
	// (string), опционально "email" и "roles" ([]string или string);
	// остальные claims доступны через headers.claim_to_header.
	Authenticate(r *http.Request) (map[string]any, error)
}

// AuthConnectors возвращает скомпилированные auth-коннекторы по имени модуля.
func AuthConnectors() map[string]AuthConnector {
	mods := Modules()
	out := make(map[string]AuthConnector)
	for _, m := range mods {
		if c, ok := m.(AuthConnector); ok {
			out[m.Name()] = c
		}
	}
	return out
}
