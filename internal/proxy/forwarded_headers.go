package proxy

import "net/http"

// applyForwardedHeaders приводит forwarding-заголовки к виду, которому
// бэкенд может доверять. Клиентские значения X-Forwarded-* не должны
// доходить до бэкенда:
//
//   - X-Forwarded-For: цепочку принимаем только от доверенного пира
//     (server.trusted_proxies). Недоверенному пиру цепочка не верится —
//     заголовок удаляется, и httputil.ReverseProxy (Director-режим) сам
//     добавляет реальный IP пира без порта.
//   - X-Forwarded-Host: всегда публичный Host входящего запроса.
//   - X-Forwarded-Proto: всегда фактическая схема соединения с шлюзом.
//
// Вызывается до возможных early-return в modifyRequest: иначе на маршруте
// с необязательной аутентификацией клиентский X-Forwarded-Host проходил бы
// насквозь.
func (mp *MultiProxy) applyForwardedHeaders(r *http.Request) {
	if !mp.peerIsTrustedProxy(r) {
		r.Header.Del("X-Forwarded-For")
	}
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-Proto", requestScheme(r))
}

// requestScheme — схема, по которой клиент соединился именно с шлюзом.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
