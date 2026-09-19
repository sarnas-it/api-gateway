package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	fmt.Println("webhook sink on :19902")
	panic(http.ListenAndServe("127.0.0.1:19902", nil))
}
