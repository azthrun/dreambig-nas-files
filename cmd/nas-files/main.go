// Command nas-files is the NAS Files web file manager (ADR-0003).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/azthrun/dreambig-nas-files/internal/access"
	"github.com/azthrun/dreambig-nas-files/internal/config"
	"github.com/azthrun/dreambig-nas-files/internal/identity"
	"github.com/azthrun/dreambig-nas-files/internal/mounts"
	"github.com/azthrun/dreambig-nas-files/internal/server"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on NAS_FILES_ADDR and exit 0 if it answers 200")
	flag.Parse()

	cfg, cfgErr := config.Load(os.Getenv)
	if *healthcheck {
		// Only Addr is needed; Load sets it even when the rest of the config is bad.
		if err := probe(cfg.Addr); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck:", err)
			os.Exit(1)
		}
		return
	}

	// Operational logs go to stderr, the audit log to stdout.
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if cfgErr != nil {
		log.Error("refusing to start: bad config", "err", cfgErr)
		os.Exit(1)
	}
	if err := run(cfg, log); err != nil {
		log.Error("refusing to start or server stopped", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ms, err := mounts.Discover(cfg.Root, mounts.Options{RequireMountPoints: cfg.RequireMountPoints})
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Warning != "" {
			log.Warn(m.Warning, "type", "warning")
		}
	}

	auth, err := identity.Load(cfg.UsersFile)
	if err != nil {
		return err
	}

	acc, err := access.Load(cfg.AccessFile, auth.Names(), ms)
	if err != nil {
		return err
	}

	handler, err := server.New(server.Deps{Mounts: ms, Auth: auth, Access: acc, Audit: os.Stdout, Log: log})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Name
	}
	log.Info("listening", "addr", ln.Addr().String(), "mounts", names)

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// probe GETs /healthz on addr, reaching a wildcard listen address via loopback.
func probe(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse NAS_FILES_ADDR %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("GET /healthz: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz answered %d", resp.StatusCode)
	}
	return nil
}
