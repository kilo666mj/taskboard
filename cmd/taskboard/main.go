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

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/push"
	"github.com/kilo666mj/taskboard/internal/server"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := store.Open(ctx, cfg.DatabasePath)
	cancel()
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("database close failed", "error", err)
		}
	}()
	tasks := service.New(database, cfg.LeaseDuration)
	notifications := push.New(database, tasks, cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDContact, logger)
	runContext, stopNotifications := context.WithCancel(context.Background())
	notifications.Run(runContext)
	handler, err := server.New(cfg, database, tasks, notifications, logger)
	if err != nil {
		logger.Error("server startup failed", "error", err)
		os.Exit(1)
	}
	httpContext, cancelHTTPRequests := context.WithCancel(context.Background())
	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext: func(net.Listener) context.Context {
			return httpContext
		},
	}
	stopSweep := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if count, err := tasks.SweepStale(context.Background()); err != nil {
					logger.Error("stale run sweep failed", "error", err)
				} else if count > 0 {
					logger.Info("marked stale agent runs", "count", count)
				}
			case <-stopSweep:
				return
			}
		}
	}()
	go func() {
		logger.Info("taskboard listening", "address", cfg.ListenAddress)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopNotifications()
	close(stopSweep)
	cancelHTTPRequests()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
	notifications.Wait()
}
