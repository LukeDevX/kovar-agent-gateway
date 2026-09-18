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

	"kovar-gateway/internal/app"
	"kovar-gateway/internal/platform/config"
	"kovar-gateway/internal/platform/database"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var level slog.Level
	if err = level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return errors.New("invalid LOG_LEVEL")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(root, 20*time.Second)
	defer cancel()
	db, err := database.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return errors.New("database connection failed; check configured credentials and connectivity")
	}
	defer db.Close()
	gateway, err := app.New(db, cfg, log)
	if err != nil {
		return errors.New("gateway initialization failed; validate router and encryption configuration")
	}
	if err = gateway.Service.Identity.Bootstrap(startup); err != nil {
		return errors.New("admin bootstrap failed; run migrations before starting gateway")
	}
	server := &http.Server{Addr: cfg.Addr, Handler: gateway.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: cfg.RequestTimeout + 20*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return root }, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	log.Info("gateway started", "address", cfg.Addr, "environment", cfg.Env)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err := <-errorsCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return errors.New("HTTP listener failed")
		case <-root.Done():
			shutdown, finish := context.WithTimeout(context.WithoutCancel(root), 10*time.Second)
			defer finish()
			if err = server.Shutdown(shutdown); err != nil {
				_ = server.Close()
				return errors.New("HTTP graceful shutdown timed out")
			}
			return nil
		case <-ticker.C:
			ctx, done := context.WithTimeout(root, 10*time.Second)
			err = gateway.Service.Cleanup(ctx)
			done()
			if err != nil && root.Err() == nil {
				log.Error("expired authentication state cleanup failed")
			}
		}
	}
}
