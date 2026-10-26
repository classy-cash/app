// Command classycash is the Classy Cash server and its operator CLI.
//
//	classycash [serve]          run the web application (default)
//	classycash migrate          apply pending database migrations and exit
//	classycash backup FILE      write a consistent, verified copy of the database (SQLite VACUUM INTO)
//	classycash demo-load [FILE] DEMO_MODE only: atomically replace all data with seed SQL (stdin if no FILE)
//	classycash ledger-rebuild   recompute every class cash ledger
//	classycash config-check     validate the environment and print the effective, secret-free configuration
//	classycash version
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/jobs"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/internal/version"
	"github.com/classy-cash/app/internal/web"
	"github.com/classy-cash/app/migrations"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd == "version" || cmd == "--version" {
		fmt.Printf("classycash %s (commit %s, built %s, config v%d)\n", version.Version, version.Commit, version.Date, version.ConfigVersion)
		return
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(2)
	}
	log := newLogger(cfg)
	if err := run(cmd, args, cfg, log); err != nil {
		log.Error("failed", "command", cmd, "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func open(cfg *config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o750); err != nil {
		return nil, err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(conn, migrations.FS); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func run(cmd string, args []string, cfg *config.Config, log *slog.Logger) error {
	switch cmd {
	case "config-check":
		return configCheck(cfg)
	case "migrate":
		conn, err := open(cfg)
		if err != nil {
			return err
		}
		defer conn.Close()
		names, _ := db.Names(migrations.FS)
		fmt.Printf("database %s is up to date (%d migrations)\n", cfg.DBPath, len(names))
		return nil
	case "backup":
		if len(args) != 1 {
			return errors.New("usage: classycash backup FILE")
		}
		return backup(cfg, args[0])
	case "demo-load":
		return demoLoad(cfg, args)
	case "ledger-rebuild":
		conn, err := open(cfg)
		if err != nil {
			return err
		}
		defer conn.Close()
		return store.New(conn, cfg.Location).RecalculateLedger(context.Background())
	case "serve":
		return serve(cfg, log)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func configCheck(cfg *config.Config) error {
	out := map[string]any{
		"config_version": version.ConfigVersion, "env": cfg.Env, "addr": cfg.Addr, "base_url": cfg.BaseURL, "db_path": cfg.DBPath,
		"instance_id": cfg.InstanceID, "timezone": cfg.Location.String(), "memory_limit_mb": cfg.MemLimitMB, "demo_mode": cfg.Demo,
		"mail_driver": cfg.Mail.Driver, "mail_relay_configured": cfg.Mail.RelayURL != "",
		"bank_webhook_enabled": cfg.Bank.Enabled(), "bank_address": cfg.Bank.Address, "claim_token_set": cfg.ClaimToken != "",
		"trust_proxy": cfg.TrustProx,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// backup writes a transactionally consistent copy using VACUUM INTO and verifies it.
func backup(cfg *config.Config, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", dest)
	}
	conn, err := open(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if _, err := conn.Exec(`VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return err
	}
	check, err := db.Open(dest)
	if err != nil {
		return err
	}
	defer check.Close()
	var res string
	if err := check.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		_ = os.Remove(dest)
		return fmt.Errorf("backup failed verification: %v %s", err, res)
	}
	fmt.Printf("backup written and verified: %s\n", dest)
	return nil
}

func demoLoad(cfg *config.Config, args []string) error {
	if !cfg.Demo {
		return errors.New("refusing to replace data: CLASSYCASH_DEMO_MODE is not enabled")
	}
	var r io.Reader = os.Stdin
	if len(args) > 0 && args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	seed, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return err
	}
	conn, err := open(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	gen, err := store.New(conn, cfg.Location).DemoLoad(context.Background(), string(seed))
	if err != nil {
		return err
	}
	fmt.Printf("{\"generation\":%d,\"loaded_at\":%q}\n", gen, time.Now().UTC().Format(time.RFC3339))
	return nil
}

func serve(cfg *config.Config, log *slog.Logger) error {
	if cfg.MemLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.MemLimitMB) << 20) // soft limit: the GC works harder before the host's 256 MB is hit
	}
	conn, err := open(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	st := store.New(conn, cfg.Location)
	st.BaseURL = cfg.BaseURL

	var sender mail.Sender = mail.CaptureSender{Log: log}
	if cfg.Mail.Driver == "relay" {
		sender = mail.NewRelaySender(cfg.Mail.RelayURL, cfg.Mail.RelayKey, cfg.Mail.FromName)
	}
	srv, err := web.New(cfg, st, log, sender, migrations.FS)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr: cfg.Addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runner := &jobs.Runner{Store: st, Sender: sender, Log: log}
	jobsDone := make(chan struct{})
	go func() { defer close(jobsDone); runner.Run(ctx) }()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("classycash started", "addr", cfg.Addr, "version", version.Version, "env", cfg.Env, "demo", cfg.Demo, "mail", cfg.Mail.Driver, "bank_webhook", cfg.Bank.Enabled())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	<-jobsDone
	_, _ = conn.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
