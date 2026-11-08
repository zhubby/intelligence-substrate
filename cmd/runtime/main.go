package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/runtimehost"
)

func main() {
	configFile := flag.String("config", "", "Trusted runtime configuration")
	mode := flag.String("mode", "publish", "init, publish, status, rollback, gateway, monitor, inspect or reconcile")
	listen := flag.String("listen", "127.0.0.1:8091", "Private gateway address")
	key := flag.String("operation-key", "", "Operation to inspect or reconcile")
	keep := flag.String("keep", "", "Explicit reconciliation: previous or candidate")
	once := flag.Bool("once", false, "Run one persisted monitoring check")
	flag.Parse()
	body, err := os.ReadFile(*configFile)
	check(err)
	var config runtimehost.Config
	check(json.Unmarshal(body, &config))
	readOnly := *mode == "gateway" || *mode == "inspect"
	store, err := runtimehost.Open(config, readOnly)
	check(err)
	defer store.Close()
	engine := runtimehost.Engine{Store: store, Driver: runtimehost.Docker{Config: config}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch *mode {
	case "init":
		return
	case "inspect":
		op, err := store.Inspect(ctx, *key)
		check(err)
		check(json.NewEncoder(os.Stdout).Encode(op))
	case "reconcile":
		op, err := engine.Reconcile(ctx, *key, *keep)
		check(err)
		check(json.NewEncoder(os.Stdout).Encode(op))
	case "publish", "status", "rollback":
		var request runtimehost.Request
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
		decoder.DisallowUnknownFields()
		check(decoder.Decode(&request))
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			check(fmt.Errorf("exactly one publication request required"))
		}
		if request.Action != *mode {
			check(fmt.Errorf("runtime mode does not authorize the requested action"))
		}
		result, err := engine.Execute(ctx, request)
		check(err)
		check(json.NewEncoder(os.Stdout).Encode(result))
	case "monitor":
		for ctx.Err() == nil {
			err := engine.MonitorOnce(ctx)
			if err != nil {
				slog.Error("runtime observation pending", "error", err)
			}
			if *once {
				check(err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	case "gateway":
		host, _, err := net.SplitHostPort(*listen)
		check(err)
		if host != "127.0.0.1" && host != "::1" {
			check(fmt.Errorf("gateway must bind private loopback behind a dedicated TLS origin"))
		}
		listener, err := net.Listen("tcp", *listen)
		check(err)
		server := &http.Server{Handler: runtimehost.Gateway(store), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		slog.Info("runtime gateway listening", "address", listener.Addr().String())
		go func() {
			<-ctx.Done()
			deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(deadline)
		}()
		if err = server.Serve(listener); err != http.ErrServerClosed {
			check(err)
		}
	default:
		check(fmt.Errorf("unknown runtime mode"))
	}
}
func check(err error) {
	if err != nil {
		slog.Error("runtime operation failed", "error", err)
		os.Exit(1)
	}
}
