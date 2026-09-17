package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/api"
	"github.com/mhmdxsadk/snatcher/internal/client"
	"github.com/mhmdxsadk/snatcher/internal/config"
	"github.com/mhmdxsadk/snatcher/internal/security"
	"github.com/mhmdxsadk/snatcher/internal/version"
)

func main() {
	if err := run(); err != nil {
		slog.Error("snatcher stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	c, err := client.New(cfg.CobaltURL)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           security.New(cfg.Security).Wrap(api.NewHandler(c)),
		MaxHeaderBytes:    16 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	slog.Info("snatcher starting", "version", version.Release, "api", version.API, "address", listener.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stop()
		// Give active requests and transfers up to 45 seconds to finish.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
