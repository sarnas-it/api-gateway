package proxy

import (
	"regexp"
	"testing"
)

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ID запроса уходит клиенту и используется как ключ обращения, поэтому формат
// фиксирован: 128 бит в hex.
func TestGenerateRequestID_Format(t *testing.T) {
	id := generateRequestID()
	if !requestIDPattern.MatchString(id) {
		t.Fatalf("request id %q не соответствует ^[0-9a-f]{32}$", id)
	}
}

// Два прогона по 10 тысяч ID не должны пересекаться: иначе ID предсказуем
// или генерируется из слишком маленького пространства.
func TestGenerateRequestID_Unique(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := generateRequestID()
		if _, ok := seen[id]; ok {
			t.Fatalf("повтор request id %q на позиции %d", id, i)
		}
		seen[id] = struct{}{}
	}
}
