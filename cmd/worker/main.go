package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"os"
	"os/signal"
	agentv1 "protobuf/gen/agent/v1"
	"protobuf/internal/config"
	"protobuf/internal/core"
	"protobuf/internal/jobs"
	"protobuf/internal/platform"
	"protobuf/internal/store"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
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
	if err = r.Ping(ctx).Err(); err != nil {
		return err
	}
	conn, err := jobs.ConnectAgent(c)
	if err != nil {
		return err
	}
	defer conn.Close()
	s := &core.Service{DB: db}
	worker := &jobs.Runner{DB: db, Redis: r, Core: s, Agent: agentv1.NewAgentServiceClient(conn), Sender: platform.New(c, s, r), Token: c.AgentToken}
	slog.Info("worker started")
	return worker.Run(ctx)
}
