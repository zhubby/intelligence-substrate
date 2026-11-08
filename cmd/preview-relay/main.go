// This trusted static Linux binary is the preview's container supervisor and
// one-request stdin/stdout relay. It has no configurable network destination.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/previewnode"
)

func run() error {
	if len(os.Args) == 2 && os.Args[1] == "serve" {
		deadline, err := time.Parse(time.RFC3339Nano, os.Getenv("SUBSTRATE_PREVIEW_EXPIRES_AT"))
		if err != nil || !deadline.After(time.Now()) || time.Until(deadline) > 4*time.Hour {
			return fmt.Errorf("bounded preview expiry required")
		}
		signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithDeadline(signals, deadline)
		defer cancel()
		child := exec.CommandContext(ctx, "/substrate-backend")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.WaitDelay = 3 * time.Second
		// Docker ends all remaining descendants when this PID 1 exits.
		err = child.Run()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("unsupported relay arguments")
	}
	var q previewnode.Request
	dec := json.NewDecoder(io.LimitReader(os.Stdin, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&q) != nil || dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid bounded relay request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	result, err := previewnode.Relay(ctx, nil, os.Getenv("APP_MODE"), q)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "preview relay failed")
		os.Exit(1)
	}
}
