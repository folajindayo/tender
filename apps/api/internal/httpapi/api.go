// Package httpapi exposes the settlement service over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
	"tender/api/internal/vision"
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
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			if origin == "" || origin == "null" {
				return true
			}
			if a.Cfg.CORSOrigin != "" && (origin == a.Cfg.CORSOrigin || strings.TrimRight(origin, "/") == strings.TrimRight(a.Cfg.CORSOrigin, "/")) {
				return true
			}
			// Allow all Vercel deployments and localhost ports
			if strings.HasSuffix(origin, ".vercel.app") ||
				origin == "https://tender-pwa.vercel.app" ||
				strings.HasPrefix(origin, "http://localhost:") ||
				strings.HasPrefix(origin, "http://127.0.0.1:") {
				return true
			}
			return false
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowCredentials: true,
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Tender-User", "X-Requested-With", "Origin"},
		ExposedHeaders:   []string{"Link", "Set-Cookie"},
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
		// There is deliberately no route listing every user. Nothing in the app
		// needs a directory of who holds an account, and publishing names,
		// cities and balances is not a thing to do by accident.
		r.Get("/users/{id}", a.getUser)
		r.Get("/users/{id}/transfers", a.userTransfers)

		r.Post("/auth/signup", a.signup)
		r.Post("/auth/signin", a.signin)
		r.Post("/auth/signout", a.signout)
		r.Get("/auth/me", a.me)

		r.Get("/banks", a.listBanks)
		r.Get("/float", a.floatStatus)
		r.Post("/float/fund", a.fundFloat)
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
	// A recognizer that is not answering is not an internal fault the sender can
	// retry past, so it does not get the generic "try again". The detail stays
	// in the log; the sender is told the photograph is not what is wrong.
	if errors.Is(err, vision.ErrUnavailable) {
		slog.Error("vision unavailable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"accepted": false,
			"code":     "vision_unavailable",
			"reason": "We cannot count notes right now -- the recogniser is unavailable. " +
				"Your photo is fine; nothing has been pledged. Please try later.",
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
