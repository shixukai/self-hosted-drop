package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/shixukai/self-hosted-drop/internal/app"
	"github.com/shixukai/self-hosted-drop/internal/config"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "init-secrets" {
		dir := ".secrets"
		if len(os.Args) > 2 {
			dir = os.Args[2]
		}
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		for _, name := range []string{"setup.key", "internal.key", "postgres.key", "worker.key", "restic.key"} {
			b := make([]byte, 32)
			if _, e := rand.Read(b); e != nil {
				return e
			}
			f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			_, e = f.WriteString(hex.EncodeToString(b))
			f.Close()
			if e != nil {
				return e
			}
		}
		fmt.Println("Created secret files in", dir)
		return nil
	}
	c, e := config.Load()
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, e := app.New(ctx, c)
	if e != nil {
		return fmt.Errorf("database initialization failed: %w", e)
	}
	defer a.Close()
	switch command {
	case "migrate":
		fmt.Println("Database migrations applied")
		return nil
	case "reset-password":
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 257))
		if e != nil {
			return e
		}
		return a.ResetPassword(ctx, strings.TrimSpace(string(b)))
	case "backup":
		return a.Backup(ctx)
	case "doctor":
		return a.Doctor(ctx)
	case "reconcile":
		flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
		mode := flags.String("mode", "inspect", "inspect, complete, or restore-applied")
		hash := flags.String("expected-config-hash", "", "gatewayConfigHash from the preceding inspection")
		if e = flags.Parse(os.Args[2:]); e != nil {
			return e
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("usage: drop reconcile [--mode complete|restore-applied --expected-config-hash SHA256] publication-id")
		}
		recoveryCtx, finish := context.WithTimeout(ctx, 10*time.Minute)
		defer finish()
		report, err := a.Reconcile(recoveryCtx, flags.Arg(0), app.ReconcileOptions{Mode: *mode, ExpectedConfigHash: *hash})
		if e = json.NewEncoder(os.Stdout).Encode(report); e != nil {
			return e
		}
		return err
	case "gc":
		return a.GC(ctx, len(os.Args) > 2 && os.Args[2] == "--dry-run")
	case "serve":
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
	a.Run(ctx)
	servers := []*http.Server{{Addr: c.Listen, Handler: a.Router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Minute, WriteTimeout: 15 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}, {Addr: c.InternalListen, Handler: a.InternalRouter(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}}
	errs := make(chan error, 2)
	for _, s := range servers {
		go func(s *http.Server) { errs <- s.ListenAndServe() }(s)
	}
	a.Log.Info("Drop started", "adminOrigin", c.AdminOrigin)
	select {
	case e = <-errs:
		if e != http.ErrServerClosed {
			return e
		}
	case <-ctx.Done():
	}
	cancel()
	stop, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	for _, s := range servers {
		s.Shutdown(stop)
	}
	return nil
}
