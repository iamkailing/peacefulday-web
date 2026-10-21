// Command server runs the Peaceful Day website, booking engine and admin.
//
//	server                      start the web server (runs migrations first)
//	server -migrate             apply database migrations and exit
//	server -seed-demo           add clearly-labelled demo data (not allowed in production)
//	server -purge-demo          remove all demo data
//	server -reset-password EMAIL   set a staff password from the NEW_PASSWORD env var
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // Asia/Bangkok works even on minimal container images

	"peacefulday/internal/app"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "apply migrations and exit")
	seedDemo := flag.Bool("seed-demo", false, "insert demo data (development/staging only) and exit")
	purgeDemo := flag.Bool("purge-demo", false, "delete demo data and exit")
	resetPW := flag.String("reset-password", "", "staff email whose password is set from NEW_PASSWORD, then exit")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := app.LoadConfig()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := app.OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := app.Migrate(ctx, db); err != nil {
		log.Error("migration failed", "err", err)
		os.Exit(1)
	}
	if *migrateOnly {
		log.Info("migrations applied")
		return
	}
	if err := app.SeedBase(ctx, db, log); err != nil {
		log.Error("seed failed", "err", err)
		os.Exit(1)
	}
	if err := app.EnsureOwner(ctx, db, cfg, log); err != nil {
		log.Error("owner bootstrap failed", "err", err)
		os.Exit(1)
	}
	switch {
	case *seedDemo:
		exitOn(log, app.SeedDemo(ctx, db, cfg, log), "demo data created")
		return
	case *purgeDemo:
		exitOn(log, app.PurgeDemo(ctx, db), "demo data removed")
		return
	case *resetPW != "":
		exitOn(log, app.ResetStaffPassword(ctx, db, *resetPW, os.Getenv("NEW_PASSWORD")), "password updated; all sessions for that user were signed out")
		return
	}

	a, err := app.New(cfg, db, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           a,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if !cfg.DisableWorkers {
		go a.RunWorkers(ctx)
	}
	go func() {
		log.Info("listening", "addr", cfg.Addr, "env", cfg.Env, "payments", a.Pay.Name(), "payment_test_mode", cfg.PaymentTestMode(), "base_url", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			cancel()
		}
	}()
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
	defer done()
	srv.Shutdown(shutdownCtx)
}

func exitOn(log *slog.Logger, err error, okMsg string) {
	if err != nil {
		log.Error("command failed", "err", err)
		os.Exit(1)
	}
	log.Info(okMsg)
}
