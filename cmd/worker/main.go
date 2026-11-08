package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/integrations"
	"github.com/intelligence-substrate/platform/internal/worker"
)

func main() {
	configPath := flag.String("config", "", "Administrator-controlled worker configuration JSON")
	once := flag.Bool("once", false, "Process at most one job")
	flag.Parse()
	config := worker.Config{Driver: "container", StateDir: ".worker-state", AX: integrations.AXFromEnv()}
	if *configPath != "" {
		b, e := os.ReadFile(*configPath)
		check(e)
		check(json.Unmarshal(b, &config))
	}
	token := os.Getenv("IS_WORKER_TOKEN")
	if file := os.Getenv("IS_WORKER_TOKEN_FILE"); file != "" {
		b, e := os.ReadFile(file)
		check(e)
		token = string(b)
	}
	client, e := worker.NewClient(os.Getenv("IS_PLATFORM_URL"), token, os.Getenv("IS_WORKER_ALLOW_HTTP") == "true")
	check(e)
	runner := worker.Runner{Client: client, Config: config}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for ctx.Err() == nil {
		worked, e := runner.RunOnce(ctx)
		if e != nil {
			slog.Error("worker cycle failed", "error", e)
		} else if worked {
			slog.Info("job result recorded")
		}
		if *once {
			if e != nil {
				os.Exit(1)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func check(e error) {
	if e != nil {
		slog.Error("worker startup failed", "error", e)
		os.Exit(1)
	}
}
