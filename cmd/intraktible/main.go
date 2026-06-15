// SPDX-License-Identifier: AGPL-3.0-or-later

// Command intraktible runs the platform. The same binary runs the full modular
// monolith or a subset of modules; modules can also run as separate services.
//
//	intraktible serve --modules=all
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	casecmd "github.com/e6qu/intraktible/case-manager/command"
	caseservice "github.com/e6qu/intraktible/case-manager/service"
	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	contextservice "github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	hellocmd "github.com/e6qu/intraktible/hello/command"
	helloservice "github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/web"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: intraktible serve [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	modules := fs.String("modules", "all", "comma-separated modules (or 'all')")
	devKey := fs.String("dev-api-key", "dev-sandbox-key", "seed a sandbox API key for local dev (empty to disable)")
	_ = fs.Parse(os.Args[2:])

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(*addr, *dataDir, *modules, *devKey); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir, modules, devKey string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log, err := eventlog.OpenWAL(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	st := store.NewMemory()
	keyring := auth.NewKeyring()
	sessions := auth.NewSessions()
	if devKey != "" {
		keyring.Add(devKey, auth.APIKey{
			ID:       "dev",
			Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
			Scope:    auth.Sandbox,
		})
		slog.Warn("seeded dev API key — do not use in production", "scope", auth.Sandbox)
	}

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/", web.Handler())

	api := http.NewServeMux()
	var projectors []projection.Projector

	if enabled(modules, "hello") {
		helloSvc := helloservice.New(hellocmd.NewHandler(log), st)
		helloSvc.Routes(api)
		projectors = append(projectors, stats.Projector{})
	}

	if enabled(modules, "decision-engine") {
		// Decisions can reference a Context Layer entity to fold its features into
		// the input, and a flow's Connect nodes call Context Layer connectors; both
		// providers read the shared store (no-ops when the context-layer module is
		// not running / nothing is defined).
		decide := enginecmd.NewDecideHandler(log, st,
			enginecmd.WithFeatures(features.Provider{Store: st}),
			enginecmd.WithConnectors(connectors.Provider{Store: st}))
		engineSvc := engineservice.New(enginecmd.NewHandler(log), decide, st)
		engineSvc.Routes(api)
		projectors = append(projectors, flows.Projector{}, history.Projector{}, analytics.Projector{})
	}

	if enabled(modules, "case-manager") {
		caseSvc := caseservice.New(casecmd.NewHandler(log), st)
		caseSvc.Routes(api)
		projectors = append(projectors, cases.Projector{})
	}

	if enabled(modules, "context-layer") {
		contextSvc := contextservice.New(contextcmd.NewHandler(log), st)
		contextSvc.Routes(api)
		projectors = append(projectors, entities.Projector{}, features.Projector{}, connectors.Projector{})
	}

	rt := projection.New(log, st, projectors...)
	if err := rt.Start(ctx); err != nil {
		return fmt.Errorf("projection start: %w", err)
	}

	root.Handle("/v1/", httpx.Chain(api, httpx.Authenticate(keyring, sessions)))
	handler := httpx.Chain(root, httpx.Recover, httpx.RequestID, httpx.Logger)

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "modules", modules)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case err := <-errc:
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// enabled reports whether module m should run given the --modules selection.
func enabled(modules, m string) bool {
	if modules == "all" || modules == "" {
		return true
	}
	for _, part := range splitComma(modules) {
		if part == m {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}
