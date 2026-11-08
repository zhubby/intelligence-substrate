package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/intelligence-substrate/platform/internal/blueprint"
	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func main() {
	for _, key := range []string{"IS_BOOTSTRAP_PASSWORD", "IS_MODEL_API_KEY", "IS_OIDC_CLIENT_SECRET"} {
		if path := os.Getenv(key + "_FILE"); path != "" {
			body, e := os.ReadFile(path)
			check(e)
			check(os.Setenv(key, strings.TrimSpace(string(body))))
		}
	}
	addr := flag.String("listen", env("IS_LISTEN", "127.0.0.1:8080"), "HTTP listen address")
	data := flag.String("data", env("IS_DATA_DIR", ".data"), "Private data directory")
	static := flag.String("static", env("IS_STATIC_DIR", "web/dist"), "Built workbench directory")
	blueprints := flag.String("blueprints", env("IS_BLUEPRINT_ROOT", "-"), "Installed blueprint release root; - disables the catalog")
	flag.Parse()
	abs, e := filepath.Abs(*data)
	check(e)
	check(os.MkdirAll(abs, 0700))
	check(os.Setenv("IS_DATA_DIR", abs))
	dsn := os.Getenv("IS_DATABASE_URL")
	if dsn == "" {
		dsn = filepath.Join(abs, "platform.db")
	}
	store, e := p.Open(dsn)
	check(e)
	defer store.Close()
	service := p.NewService(store)
	service.Blueprints, e = blueprint.Load(*blueprints)
	check(e)
	dev := os.Getenv("IS_DEV_MODE") == "true"
	if !dev {
		check(validatePublicURL(os.Getenv("IS_PUBLIC_URL")))
	}
	h := httpapi.New(service, !dev)
	h.Static = *static
	h.PublicURL = os.Getenv("IS_PUBLIC_URL")
	previewListen := os.Getenv("IS_PREVIEW_LISTEN")
	check(h.ConfigurePreviews(previewListen, os.Getenv("IS_PREVIEW_URL"), dev))
	check(h.ConfigurePreviewNode(os.Getenv("IS_PREVIEW_NODE_SOCKET")))
	password := os.Getenv("IS_BOOTSTRAP_PASSWORD")
	var n int
	check(store.DB.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n))
	if n == 0 && password == "" {
		if !dev {
			slog.Error("Set IS_BOOTSTRAP_PASSWORD (at least 14 characters) for first startup")
			os.Exit(1)
		}
		password = p.Secret()
		check(os.WriteFile(filepath.Join(abs, "bootstrap-password"), []byte(password), 0600))
		slog.Info("Development bootstrap password saved", "path", filepath.Join(abs, "bootstrap-password"))
	}
	if n == 0 {
		check(h.Auth.Bootstrap(env("IS_BOOTSTRAP_EMAIL", "admin@localhost.test"), password))
	}
	model := integrations.ModelFromEnv(service)
	h.Integrations = func() []p.M { return integrations.Capabilities(model != nil) }
	if model != nil {
		h.GeneratePlan = model.GeneratePlan
		h.GenerateClarifications = model.GenerateClarifications
		h.GenerateRequirements = model.GenerateRequirements
		h.ModelCall = model.Chat
	}
	check(h.ConfigureOIDC(context.Background()))
	server := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	previewFailure := make(chan error, 1)
	previewDone := make(chan struct{})
	var previewServer *http.Server
	if h.PreviewURL != "" {
		listener, err := net.Listen("tcp", previewListen)
		check(err)
		previewServer = &http.Server{Handler: h.PreviewGateway(), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		go func() {
			defer close(previewDone)
			if err := previewServer.Serve(listener); err != nil && err != http.ErrServerClosed {
				previewFailure <- err
				stop()
			}
		}()
	} else {
		close(previewDone)
	}
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := service.ReconcileRuntimeSignals(checkCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("runtime signal watchdog failed", "error", err)
			}
			waitCtx, cancelWaits := context.WithTimeout(ctx, 10*time.Second)
			err = service.ReconcileMessageWaits(waitCtx)
			cancelWaits()
			if err != nil && ctx.Err() == nil {
				slog.Error("collaboration wait maintenance failed", "error", err)
			}
			previewCtx, cancelPreviews := context.WithTimeout(ctx, 10*time.Second)
			err = h.ReconcilePreviewRuntimes(previewCtx)
			cancelPreviews()
			if err != nil && ctx.Err() == nil {
				slog.Error("preview runtime maintenance requires inspection")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if previewServer != nil {
			_ = previewServer.Shutdown(c)
		}
		_ = server.Shutdown(c)
	}()
	slog.Info("Intelligence Substrate listening", "address", *addr, "development", dev)
	e = server.ListenAndServe()
	stop()
	<-watchdogDone
	<-previewDone
	select {
	case err := <-previewFailure:
		check(err)
	default:
	}
	if e != nil && e != http.ErrServerClosed {
		check(e)
	}
}

func validatePublicURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("production IS_PUBLIC_URL must be an HTTPS origin without credentials, a path, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("production IS_PUBLIC_URL has an invalid port")
		}
	}
	return nil
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func check(e error) {
	if e != nil {
		slog.Error("startup failed", "error", e)
		os.Exit(1)
	}
}
