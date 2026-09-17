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
	slog.Info("snatcher starting", "address", listener.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stop()
		// Allow the 10-second request read and 30-second Cobalt timeout to finish.
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
