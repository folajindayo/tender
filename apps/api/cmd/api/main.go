// Command api serves the Tender settlement layer.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tender/api/internal/config"
	"tender/api/internal/fintava"
	"tender/api/internal/httpapi"
	"tender/api/internal/migrate"
	"tender/api/internal/payout"
	"tender/api/internal/settle"
	"tender/api/internal/store"
	"tender/api/internal/stream"
	"tender/api/internal/vision"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("cannot reach the database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Bring the schema up before serving. On hosts without a pre-deploy hook
	// this is the only place it can happen, and it is idempotent and locked, so
	// running it on every boot costs nothing.
	if cfg.MigrateOnStart {
		if err := migrate.Up(ctx, st.Pool); err != nil {
			slog.Error("migrations failed", "err", err)
			os.Exit(1)
		}
	}

	provider := pickVision(cfg)
	hub := stream.NewHub()

	bank := fintava.New(fintava.Config{
		BaseURL:       cfg.FintavaBaseURL,
		APIKey:        cfg.FintavaAPIKey,
		SourceID:      cfg.FintavaSourceID,
		WebhookSecret: cfg.FintavaWebhookSecret,
	})
	payouts := &payout.Service{Pool: st.Pool, Client: bank, Hub: hub}

	svc := &settle.Service{Store: st, Vision: provider, Hub: hub, Payouts: payouts, Cfg: cfg}

	// The sweeper is what makes an abandoned handover cost nobody anything.
	go svc.RunSweeper(ctx, cfg.SweepInterval)

	// Payouts are pushed and reconciled on their own clock, so a settlement is
	// never blocked on the bank rail being reachable at that instant.
	if bank.CanPayOut() {
		go payouts.Run(ctx, cfg.PayoutInterval, cfg.PayoutStaleAfter)
	} else {
		slog.Warn("bank payouts are not configured: transfers to bank accounts will be refused")
	}

	api := &httpapi.API{
		Store: st, Service: svc, Hub: hub, Cfg: cfg,
		Fintava: bank,
		Payouts: payouts,
		Banks:   httpapi.NewBankCache(),
		// A name enquiry reveals somebody's name from their account number, and
		// a signin attempt is a password guess. Neither should be free to
		// repeat.
		Lookups: httpapi.NewLimiter(20, time.Minute),
		Signins: httpapi.NewLimiter(10, time.Minute),
	}
	go api.SweepSessions(ctx)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous: a pledge carries a photograph and waits on vision.
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 0, // SSE connections are long-lived
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("tender api listening", "port", cfg.Port,
			"vision", provider.Mode(), "model", cfg.VisionModel)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// pickVision selects the recognizer. Configuration has already rejected a
// "claude" mode with no API key, so there is no silent downgrade here.
func pickVision(cfg config.Config) vision.Provider {
	if cfg.VisionMode == "stub" {
		slog.Warn("running with the stub recognizer: no fraud screening is being performed")
		return vision.Stub{}
	}
	return vision.NewClaude(cfg.AnthropicAPIKey, cfg.VisionModel)
}
