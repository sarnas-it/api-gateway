package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/discovery"
	"github.com/basili4-1982/api-gateway/internal/logger"
	"github.com/basili4-1982/api-gateway/internal/proxy"
)

func main() {
	configPath := flag.String("config", "/etc/proxy/config.yaml", "path to config file")
	check := flag.Bool("check", false, "validate config and exit (0 = valid, 1 = invalid)")
	strict := flag.Bool("strict", false, "with -check: treat unknown-key warnings as errors")
	flag.Parse()

	if *check {
		os.Exit(runCheck(*configPath, *strict, os.Stdout, os.Stderr))
	}

	// pprof — только если явно включён через env, наружу не слушает по умолчанию.
	if addr := os.Getenv("PPROF_ADDR"); addr != "" {
		go func() {
			fmt.Fprintf(os.Stderr, "pprof listening on %s\n", addr)
			fmt.Fprintln(os.Stderr, http.ListenAndServe(addr, nil))
		}()
	}

	cfg, warnings, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.NewZapLogger(&cfg.Logging)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}

	for _, key := range warnings {
		log.Warn("Unknown config key", zap.String("key", key))
	}
	defer func(log *zap.Logger) {
		err := log.Sync()
		if err != nil {
			_, err := fmt.Fprintf(os.Stderr, "Failed to sync logger: %v\n", err)
			if err != nil {
				return
			}
		}
	}(log)

	p, err := proxy.NewMultiProxy(cfg, log)
	if err != nil {
		log.Error("Failed to create proxy", zap.Error(err))
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mgr *discovery.Manager
	if be := p.Plugins(); be != nil && be.Discovery != nil {
		// cfg.Discovery остаётся нетронутым: менеджер сохраняет state_file и
		// validate принимает discovery-only конфиг (без статических targets).
		// Встроенный docker-провайдер не строится — используется plugin-провайдер.
		mgr, err = discovery.NewManagerWithProvider(cfg, log, func(updated *config.Config) error {
			return p.Reload(updated)
		}, discovery.NewPluginProvider(be.Discovery))
		if err != nil {
			log.Error("Failed to create discovery manager", zap.Error(err))
			os.Exit(1)
		}
	} else {
		mgr, err = discovery.NewManager(cfg, log, func(updated *config.Config) error {
			return p.Reload(updated)
		})
		if err != nil {
			log.Error("Failed to create discovery manager", zap.Error(err))
			os.Exit(1)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	serverErrors := make(chan error, 1)

	go func() {
		log.Info("Multi-target proxy server starting...")
		if err := p.Start(); err != nil {
			serverErrors <- err
		}
	}()

	if err := mgr.Start(ctx); err != nil {
		log.Error("Failed to start discovery", zap.Error(err))
	}

	log.Info("Multi-target JWT Proxy is running.",
		zap.Int("port", cfg.Server.Port),
		zap.Int("targets", len(cfg.Targets)),
	)

	for {
		select {
		case err := <-serverErrors:
			log.Error("Proxy server failed", zap.Error(err))
			os.Exit(1)

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				log.Info("Received SIGHUP, reloading configuration...")
				newCfg, warnings, err := config.Load(*configPath)
				if err != nil {
					log.Error("Failed to reload config", zap.Error(err))
					continue
				}
				for _, key := range warnings {
					log.Warn("Unknown config key", zap.String("key", key))
				}
				// SetBase — единственный путь: он пересобирает конфиг с последним
				// discovery-результатом и сам вызывает Reload (работает и при
				// выключенном discovery), поэтому отдельный p.Reload не нужен.
				mgr.SetBase(newCfg)
			default:
				log.Info("Received shutdown signal", zap.String("signal", sig.String()))
				goto shutdown
			}
		}
	}

shutdown:
	log.Info("Shutting down multi-target JWT Proxy...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := mgr.Stop(); err != nil {
		log.Error("Error stopping discovery", zap.Error(err))
	}

	if err := p.Stop(shutdownCtx); err != nil {
		log.Error("Error during shutdown", zap.Error(err))
	}

	log.Info("Proxy server stopped")
}
