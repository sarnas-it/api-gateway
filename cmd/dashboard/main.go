package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"code.sarnas.ru/api-gateway/internal/dashboard"
)

func main() {
	configPath := flag.String("config", "/etc/proxy/config.yaml", "path to the gateway config file to summarize")
	discoveryState := flag.String("discovery-state", "", "path to the gateway discovery state file (empty disables the panel)")
	metricsURL := flag.String("metrics-url", "http://127.0.0.1:8080/metrics", "gateway /metrics URL (empty disables the panel)")
	listen := flag.String("listen", "127.0.0.1:8081", "dashboard listen address")
	refresh := flag.Duration("refresh", 5*time.Second, "HTML auto-refresh interval")
	basicAuth := flag.String("basic-auth", "", "optional user:password Basic Auth for all routes")
	flag.Parse()

	if err := validateBasicAuth(*basicAuth); err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := dashboard.NewServer(dashboard.ServerConfig{
		ConfigPath:     *configPath,
		DiscoveryState: *discoveryState,
		MetricsURL:     *metricsURL,
		Refresh:        *refresh,
		BasicAuth:      *basicAuth,
	})
	if err := run(ctx, *listen, srv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: %v\n", err)
		os.Exit(1)
	}
}

// validateBasicAuth rejects a non-empty -basic-auth value that is not in the
// "user:password" form. The value itself is never echoed back: it is a secret.
func validateBasicAuth(v string) error {
	if v == "" {
		return nil
	}
	if !strings.Contains(v, ":") {
		return errors.New("-basic-auth must be in user:password form (missing ':')")
	}
	return nil
}

func run(ctx context.Context, listen string, srv *dashboard.Server, out io.Writer) error {
	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "dashboard listening on %s\n", ln.Addr())

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	fmt.Fprintln(out, "dashboard stopped")
	return nil
}
