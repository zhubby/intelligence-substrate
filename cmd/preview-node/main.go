package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/previewnode"
)

func run() error {
	root := flag.String("state", "", "Private absolute node directory (0700)")
	relay := flag.String("relay", "", "Trusted static Linux relay")
	digest := flag.String("relay-sha256", "", "Approved relay digest")
	arch := flag.String("arch", "", "Linux arm64 or amd64")
	docker := flag.String("docker", "", "Absolute Docker binary")
	host := flag.String("docker-host", "unix:///var/run/docker.sock", "Local Docker engine Unix endpoint")
	capacity := flag.Int("capacity", 10, "Maximum concurrent previews (1–100)")
	flag.Parse()
	d, err := previewnode.NewDocker(previewnode.DockerConfig{StateDir: *root, RelayPath: *relay, RelaySHA256: *digest, Arch: *arch, DockerBinary: *docker, DockerHost: *host})
	if err != nil {
		return err
	}
	engine, err := previewnode.Open(*root, d, *capacity)
	if err != nil {
		return err
	}
	socket := filepath.Join(*root, "node.sock")
	if len(socket) > 100 {
		return fmt.Errorf("node socket path exceeds 100 bytes")
	}
	// A socket left by an interrupted node is removable only after confirming no
	// listener is using it. Non-socket collisions are always preserved.
	if info, x := os.Lstat(socket); x == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("node socket path occupied")
		}
		c, x := net.DialTimeout("unix", socket, time.Second)
		if x == nil {
			c.Close()
			return fmt.Errorf("preview node already running")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: previewnode.NewHandler(engine), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			check, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if x := engine.Reap(check); x != nil {
				fmt.Fprintln(os.Stderr, "preview resource reconciliation requires inspection")
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	stop()
	<-done
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
