package main

import (
	"context"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"protobuf/internal/config"
	"protobuf/internal/core"
	"protobuf/internal/httpapi"
	"protobuf/internal/platform"
	"protobuf/internal/store"
	"syscall"
	"time"
)

func main() {
	_ = godotenv.Load()
	if err := run(); err != nil {
		slog.Error("API stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, c.DatabaseURL)
	if err != nil {
		return err
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = store.Migrate(ctx, db); err != nil {
		return err
	}
	opts, err := redis.ParseURL(c.RedisURL)
	if err != nil {
		return err
	}
	r := redis.NewClient(opts)
	defer r.Close()
	s := &core.Service{DB: db}
	p := platform.New(c, s, r)
	app := &httpapi.Server{Core: s, Redis: r, Config: c, Platforms: p}
	server := &http.Server{Addr: c.Addr, Handler: app.Router(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	failures := make(chan error, 1)
	go func() { slog.Info("API listening", "address", c.Addr); failures <- server.ListenAndServe() }()
	select {
	case err := <-failures:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
