// Package httpapi exposes the settlement service over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"tender/api/internal/config"
	"tender/api/internal/fintava"
	"tender/api/internal/payout"
	"tender/api/internal/settle"
	"tender/api/internal/store"
	"tender/api/internal/stream"
)

type API struct {
	Store   *store.Store
	Service *settle.Service
	Hub     *stream.Hub
	Cfg     config.Config

	Fintava *fintava.Client
	Payouts *payout.Service

	// Banks is cached because the list changes rarely and every send screen
	// needs it. Lookups is rate limiting for name enquiry, which is a lookup of
	// somebody's name from their account number and must not be free to spam.
	Banks   *bankCache
	Lookups *limiter

	// Signins is rate limiting for the password check, keyed by both address
	// and source address.
	Signins *limiter
}

// errBody is the shape every error response uses.
func errBody(err error) map[string]string { return map[string]string{"error": err.Error()} }

func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{a.Cfg.CORSOrigin, "http://localhost:3000"},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
		// The session cookie is cross-origin: the PWA and the API are on
		// different hosts, so the browser only sends it when credentials are
		// explicitly allowed on both ends.
		AllowCredentials: true,
		AllowedHeaders:   []string{"Accept", "Content-Type", "X-Tender-User"},
		MaxAge:           300,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"visionMode": a.Cfg.VisionMode,
			"time":       time.Now().UTC(),
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Post("/users", a.createUser)
		r.Get("/users", a.listUsers)
		r.Get("/users/{id}", a.getUser)
		r.Get("/users/{id}/transfers", a.userTransfers)

		r.Post("/auth/signup", a.signup)
		r.Post("/auth/signin", a.signin)
		r.Post("/auth/signout", a.signout)
		r.Get("/auth/me", a.me)

		r.Get("/banks", a.listBanks)
		r.Post("/accounts/resolve", a.resolveAccount)

		r.Get("/venues", a.listVenues)
		r.Get("/cashouts", a.listCashouts)
		r.Post("/cashouts", a.createCashout)

		r.Post("/pledge", a.pledge)
		r.Post("/transfers/{id}/match", a.rematch)
		r.Post("/transfers/{id}/confirm", a.confirm)
		r.Post("/transfers/{id}/reject", a.rejectHandover)
		r.Post("/transfers/{id}/incident", a.reportIncident)
		r.Get("/transfers/{id}", a.getTransfer)

		r.Get("/stream", a.sse)
		r.Get("/ledger/audit", a.audit)

		// Signed by the provider, not by a session: this is the one route that
		// authenticates on an HMAC over the raw body.
		r.Post("/webhooks/fintava", a.fintavaWebhook)
	})

	return r
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// fail maps a service error onto a status code. Rejections are the user's
// business and come back as 200-level refusals or 400s with a readable reason;
// anything else is a fault.
func fail(w http.ResponseWriter, err error) {
	var rej *settle.Rejection
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"accepted": false, "code": rej.Code, "reason": rej.Reason,
		})
		return
	}
	slog.Error("request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": "Something went wrong on our side. Try again.",
	})
}

func uuidParam(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}
