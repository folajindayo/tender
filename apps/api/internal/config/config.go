package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"tender/api/internal/fintava"
	"tender/api/internal/vision"
)

type Config struct {
	DatabaseURL string
	Port        string
	CORSOrigin  string

	VisionMode      string // "claude" in production; "stub" is test-only
	VisionModel     string // which model does the counting
	AnthropicAPIKey string

	FeeBPS int

	// How long a counterparty's funds stay locked waiting for a meet-up, and
	// how long a whole transfer may stay open before it is closed out.
	MatchTTL      time.Duration
	TransferTTL   time.Duration
	CreditTTL     time.Duration
	SweepInterval time.Duration

	SettlementsForCredit int
	CreditLimitKobo      int64

	// The most cash that may change hands in a single meeting. Large amounts
	// make a handover worth targeting, so big transfers are refused rather
	// than concentrated into one trip.
	MaxHandoverKobo int64

	// Fintava is the bank rail: name enquiry before a send, and the payout that
	// delivers a settled transfer. The wallet it debits is Tender's float, so
	// FintavaSourceID is what makes payouts possible at all.
	FintavaBaseURL       string
	FintavaAPIKey        string
	FintavaSourceID      string
	FintavaWebhookSecret string
	FintavaFloatPhone    string
	FintavaFloatEmail    string

	// OperatorToken guards the endpoints that write to the books by hand.
	// Empty disables them outright rather than leaving them open: an unset
	// secret must never mean "no check".
	OperatorToken string

	// DemoInstantSettle pays the recipient the moment cash is recognised, with
	// no counterparty and no handover.
	//
	// This suspends the property the whole design rests on -- that value only
	// moves after cash physically moved -- so it is off unless somebody asks
	// for it, and it announces itself at boot and on every transfer it makes.
	// It exists to show the flow end to end before the demand side has any
	// liquidity. Turning it off restores matching with no other change.
	DemoInstantSettle bool

	// How often unsent payouts are pushed, and how long a payout may sit in a
	// non-final state before it is chased with the provider.
	PayoutInterval   time.Duration
	PayoutStaleAfter time.Duration

	// Handovers are restricted to a venue's opening hours, so meetings happen
	// while premises are staffed and busy. Only the test suite turns this off,
	// so that a run at three in the morning still matches.
	EnforceVenueHours bool

	// Whether the service applies migrations on boot. On by default, because
	// the deployment target has no pre-deploy step; turn it off where schema
	// changes are managed separately.
	MigrateOnStart bool
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:          env("DATABASE_URL", "postgres://tender:tender@localhost:5433/tender?sslmode=disable"),
		Port:                 env("PORT", "8080"),
		CORSOrigin:           env("CORS_ORIGIN", "https://tender-pwa.vercel.app"),
		VisionMode:           env("VISION_MODE", "claude"),
		VisionModel:          env("VISION_MODEL", vision.DefaultModel),
		AnthropicAPIKey:      env("ANTHROPIC_API_KEY", ""),
		FeeBPS:               envInt("FEE_BPS", 50),
		MatchTTL:             envDuration("MATCH_TTL", 30*time.Minute),
		TransferTTL:          envDuration("TRANSFER_TTL", 2*time.Hour),
		CreditTTL:            envDuration("CREDIT_TTL", time.Hour),
		SweepInterval:        envDuration("SWEEP_INTERVAL", 15*time.Second),
		SettlementsForCredit: envInt("SETTLEMENTS_FOR_CREDIT", 3),
		CreditLimitKobo:      int64(envInt("CREDIT_LIMIT_KOBO", 500000)),
		MaxHandoverKobo:      int64(envInt("MAX_HANDOVER_KOBO", 10000000)),
		EnforceVenueHours:    os.Getenv("ENFORCE_VENUE_HOURS") != "false",
		MigrateOnStart:       os.Getenv("MIGRATE_ON_START") != "false",

		FintavaBaseURL:       env("FINTAVA_BASE_URL", fintava.DefaultBaseURL),
		FintavaAPIKey:        env("FINTAVA_API_KEY", ""),
		FintavaSourceID:      env("FINTAVA_SOURCE_ID", ""),
		FintavaWebhookSecret: env("FINTAVA_WEBHOOK_SECRET", ""),
		FintavaFloatPhone:    env("FINTAVA_FLOAT_PHONE", ""),
		FintavaFloatEmail:    env("FINTAVA_FLOAT_EMAIL", ""),
		OperatorToken:        env("OPERATOR_TOKEN", ""),
		DemoInstantSettle:    os.Getenv("DEMO_INSTANT_SETTLE") == "true",
		PayoutInterval:       envDuration("PAYOUT_INTERVAL", 30*time.Second),
		PayoutStaleAfter:     envDuration("PAYOUT_STALE_AFTER", 2*time.Minute),
	}

	switch c.VisionMode {
	case "claude":
		// Note recognition is load-bearing for fraud screening. Refuse to start
		// rather than quietly accept every pledge that reaches us.
		if c.AnthropicAPIKey == "" {
			return c, fmt.Errorf("ANTHROPIC_API_KEY is required when VISION_MODE=claude")
		}
	case "stub":
	default:
		return c, fmt.Errorf("VISION_MODE must be \"claude\" or \"stub\", got %q", c.VisionMode)
	}

	if c.FeeBPS < 0 || c.FeeBPS > 10000 {
		return c, fmt.Errorf("FEE_BPS must be between 0 and 10000, got %d", c.FeeBPS)
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envDuration accepts Go duration strings such as "30m", "90s" or "2h".
func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
