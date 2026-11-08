package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/distribution"
)

func main() {
	root := flag.String("root", "", "Existing distribution directory (read only)")
	listen := flag.String("listen", "127.0.0.1:8090", "HTTP listen address behind a dedicated TLS origin")
	initOnly := flag.Bool("init", false, "Initialize an empty distribution volume and exit")
	flag.Parse()
	path, err := filepath.Abs(*root)
	if err != nil || *root == "" {
		slog.Error("an existing distribution root is required")
		os.Exit(1)
	}
	if *initOnly {
		store, err := distribution.Open(path)
		if err != nil {
			slog.Error("initialization failed", "error", err)
			os.Exit(1)
		}
		store.Close()
		return
	}
	store, err := distribution.OpenReadOnly(path)
	if err != nil {
		slog.Error("distribution startup failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	server := &http.Server{Addr: *listen, Handler: store.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen failed", "error", err)
		os.Exit(1)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("public distribution service ready", "address", listener.Addr().String())
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		slog.Error("distribution stopped", "error", err)
		os.Exit(1)
	}
}
