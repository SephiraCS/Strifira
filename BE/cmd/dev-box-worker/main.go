package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ku/ku-backend/internal/devboxworker"
)

func main() {
	configPath := flag.String("config", "", "absolute path to the local worker JSON configuration")
	once := flag.Bool("once", false, "handle at most one claimed job, then exit")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if *configPath == "" || flag.NArg() != 0 {
		slog.Error("usage: dev-box-worker --config /absolute/path/worker.json [--once]")
		os.Exit(2)
	}
	if err := run(*configPath, *once); err != nil {
		slog.Error("development worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(path string, once bool) error {
	config, err := devboxworker.LoadConfig(path)
	if err != nil {
		return err
	}
	token, err := devboxworker.ReadToken(config.TokenFile)
	if err != nil {
		return err
	}
	worker, err := devboxworker.New(config, devboxworker.NewClient(config, token))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return worker.Run(ctx, once)
}
