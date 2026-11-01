// Package admin implements the small loopback admin API: status and reload.
package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"

	"code.sarnas.ru/api-gateway/internal/config"
)

type Server struct {
	cfg        config.AdminConfig
	configPath string
	reload     func() error
	status     func() map[string]any
	log        *zap.Logger
	srv        *http.Server
	ln         net.Listener
}

func New(cfg config.AdminConfig, configPath string, reload func() error, status func() map[string]any, log *zap.Logger) *Server {
	return &Server{cfg: cfg, configPath: configPath, reload: reload, status: status, log: log}
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("admin listen: %w", err)
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("admin server", zap.Error(err))
		}
	}()
	s.log.Info("admin API listening", zap.String("addr", s.cfg.Addr))
	return nil
}

func (s *Server) Shutdown() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, s.status())
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := os.ReadFile(s.configPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		sum := sha256.Sum256(raw)
		writeJSON(w, http.StatusOK, map[string]any{
			"path":   s.configPath,
			"sha256": hex.EncodeToString(sum[:]),
			"bytes":  len(raw),
		})
	})
	mux.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.reload(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
	})
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.cfg.Token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
