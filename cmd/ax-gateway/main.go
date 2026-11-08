package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/intelligence-substrate/platform/internal/axgateway"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	path := flag.String("config", "", "absolute private administrator configuration")
	flag.Parse()
	if flag.NArg() != 0 || *path == "" {
		os.Stderr.WriteString("usage: ax-gateway -config /private/path/config.json\n")
		os.Exit(2)
	}
	config, e := axgateway.LoadConfig(*path)
	if e != nil {
		os.Stderr.WriteString("gateway configuration rejected\n")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	signals := make(chan os.Signal, 1)
	if list := reloadSignals(); len(list) > 0 {
		signal.Notify(signals, list...)
		defer signal.Stop(signals)
	}
	reloads := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case reloads <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	encoder := json.NewEncoder(os.Stdout)
	e = axgateway.Run(ctx, config, reloads, func(event string) { _ = encoder.Encode(map[string]string{"event": event}) })
	if e != nil {
		os.Stderr.WriteString("gateway stopped: configuration, transport or audit unavailable\n")
		os.Exit(1)
	}
}
