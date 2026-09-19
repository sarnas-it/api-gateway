package main

import (
	"fmt"
	"net/http"
)

func main() {
	// ~200-байтовый ответ, чтобы метрика отражала накладные расходы гейтвея.
	body := make([]byte, 200)
	for i := range body {
		body[i] = 'a'
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	fmt.Println("backend on :19901")
	panic(http.ListenAndServe("127.0.0.1:19901", nil))
}
