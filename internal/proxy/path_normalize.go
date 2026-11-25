package proxy

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// errUnsafePath — путь небезопасен (traversal-сегмент, битая кодировка).
// proxyHandler отвечает на него 400, не обращаясь к бэкенду: маршрутизация
// и авторизация идут по тому же пути, поэтому «грязный» путь нельзя
// ни матчить, ни проксировать.
var errUnsafePath = errors.New("unsafe request path")

// maxPathDecodeDepth ограничивает раскрытие вложенного percent-encoding.
// Клиенты кодируют путь один-два раза; пять слоёв — уже попытка обойти
// проверку через %2525252e.
const maxPathDecodeDepth = 5

// normalizeRequestPath проверяет и при необходимости нормализует
// r.URL.Path до маршрутизации.
//
// Percent-encoding раскрывается послойно (в том числе %252e → %2e → "."):
// traversal-сегмент ".." ловится в любом слое. Обычный путь (без кодирования,
// двойных слэшей и dot-сегментов) не меняется — Path и RawPath остаются как
// есть. Подозрительный, но безопасный путь нормализуется path.Clean, RawPath
// очищается: бэкенд не должен получать закодированные слэши/точки.
func normalizeRequestPath(r *http.Request) error {
	escaped := r.URL.EscapedPath()
	if escaped == "" {
		return nil
	}

	encoded := hasEncodedPathSeparator(escaped)
	decoded := escaped
	for depth := 0; ; depth++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			// EscapedPath() на нулевом слое всегда корректно экранирован,
			// значит ошибка появилась из уже раскрытого «%» — дальше
			// раскрывать нечего.
			break
		}
		if next == decoded {
			break
		}
		if depth >= maxPathDecodeDepth {
			return errUnsafePath
		}
		decoded = next
	}

	// Traversal ищем после полного раскрытия: иначе "..%2f" даёт "..",
	// а смешанные "/" и "\" прячут его от простого Split по слэшу.
	if pathHasTraversal(decoded) {
		return errUnsafePath
	}
	// NUL и невалидный UTF-8 в пути — признак обфускации; бэкенды и
	// файловые системы трактуют такие байты по-разному.
	if strings.ContainsRune(decoded, 0) || !utf8.ValidString(decoded) {
		return errUnsafePath
	}

	suspicious := encoded || decoded != r.URL.Path || r.URL.RawPath != "" ||
		strings.Contains(decoded, "//") || hasDotSegment(decoded)
	if !suspicious {
		return nil
	}

	normalized := path.Clean(decoded)
	if normalized == "." {
		normalized = "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	// path.Clean срезает завершающий слэш, а для WebDAV "/dav/ws/" и
	// "/dav/ws" — разные ресурсы, поэтому сохраняем его.
	if normalized != "/" && strings.HasSuffix(decoded, "/") {
		normalized += "/"
	}

	r.URL.Path = normalized
	r.URL.RawPath = ""
	return nil
}

// hasEncodedPathSeparator сообщает, что в строке есть percent-encoded точка,
// слэш, обратный слэш или знак процента. Проверка идёт по исходному
// (закодированному) виду — до раскрытия, чтобы такие пути считались
// подозрительными даже когда после декодирования выглядят обычными.
func hasEncodedPathSeparator(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		hi, lo := s[i+1], s[i+2]
		switch {
		case hi == '2' && (lo == 'e' || lo == 'E'), // .
			hi == '2' && (lo == 'f' || lo == 'F'), // /
			hi == '5' && (lo == 'c' || lo == 'C'), // \
			hi == '2' && lo == '5':                // % (вложенное кодирование)
			return true
		}
	}
	return false
}

// pathHasTraversal сообщает, есть ли в пути сегмент "..". Слэш и обратный
// слэш считаются разделителями, суффикс ";" (matrix-параметр, как в Tomcat)
// тоже отбрасывается: "..;/" — это traversal.
func pathHasTraversal(p string) bool {
	segments := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for _, seg := range segments {
		if seg == ".." || strings.HasPrefix(seg, "..;") {
			return true
		}
	}
	return false
}

// hasDotSegment сообщает, что в пути есть сегмент "." — path.Clean изменит
// такой путь, значит его нужно нормализовать.
func hasDotSegment(p string) bool {
	segments := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for _, seg := range segments {
		if seg == "." {
			return true
		}
	}
	return false
}
