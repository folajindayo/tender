package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tender/api/internal/fintava"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
)

// ---------------------------------------------------------------- bank list

// listBanks serves the institutions a recipient account can belong to.
//
// The list is cached because it changes rarely and every send screen needs it;
// without the cache, opening the app would cost a provider call per user.
func (a *API) listBanks(w http.ResponseWriter, r *http.Request) {
	banks, err := a.banks(r.Context())
	switch {
	case errors.Is(err, fintava.ErrNotConfigured):
		// Not a gateway failure: nothing was configured to call. Say so plainly
		// so the send screen can explain itself instead of showing a fault.
		writeJSON(w, http.StatusServiceUnavailable,
			errBody(errors.New("bank transfers are not configured on this deployment")))
		return
	case err != nil:
		slog.Error("bank list failed", "err", err)
		writeJSON(w, http.StatusBadGateway, errBody(errors.New("could not reach the bank directory")))
		return
	}
	writeJSON(w, http.StatusOK, banks)
}

// NewBankCache builds the cache the send screen reads its bank list from.
func NewBankCache() *bankCache { return &bankCache{} }

type bankCache struct {
	mu      sync.Mutex
	banks   []fintava.Bank
	fetched time.Time
}

func (a *API) banks(ctx context.Context) ([]fintava.Bank, error) {
	a.Banks.mu.Lock()
	defer a.Banks.mu.Unlock()

	if len(a.Banks.banks) > 0 && time.Since(a.Banks.fetched) < 12*time.Hour {
		return a.Banks.banks, nil
	}
	banks, err := a.Fintava.Banks(ctx)
	if err != nil {
		// Serve a stale list rather than break the send screen: a bank's sort
		// code does not change between one day and the next.
		if len(a.Banks.banks) > 0 {
			slog.Warn("bank list refresh failed, serving cached", "err", err)
			return a.Banks.banks, nil
		}
		return nil, err
	}
	a.Banks.banks, a.Banks.fetched = banks, time.Now()
	return banks, nil
}

// ---------------------------------------------------------------- name enquiry

// resolveAccount turns an account number into the name the bank holds for it.
//
// This is the check that makes a typed account number safe: the sender reads the
// name back before any cash changes hands. It is rate limited per caller because
// an open name-enquiry endpoint is a way to harvest the name behind any account
// number in the country, one request at a time.
func (a *API) resolveAccount(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID        string `json:"userId"`
		AccountNumber string `json:"accountNumber"`
		SortCode      string `json:"sortCode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	b.AccountNumber = strings.TrimSpace(b.AccountNumber)
	b.SortCode = strings.TrimSpace(b.SortCode)
	if b.AccountNumber == "" || b.SortCode == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("account number and bank are both required")))
		return
	}

	who := b.UserID
	if who == "" {
		who = clientIP(r)
	}
	if !a.Lookups.allow(who) {
		writeJSON(w, http.StatusTooManyRequests,
			errBody(errors.New("too many account lookups; wait a moment and try again")))
		return
	}

	acct, err := a.Fintava.ResolveAccount(r.Context(), b.AccountNumber, b.SortCode)
	switch {
	case errors.Is(err, fintava.ErrNotConfigured):
		writeJSON(w, http.StatusServiceUnavailable,
			errBody(errors.New("bank lookups are not available right now")))
		return
	case errors.Is(err, fintava.ErrAccountNotFound):
		writeJSON(w, http.StatusNotFound,
			errBody(errors.New("no account found with that number at this bank")))
		return
	case errors.Is(err, fintava.ErrUnreadable):
		// The bank answered; we could not read the answer. Saying "no such
		// account" here would blame the sender for our own decoding gap, and
		// they would retype a correct number over and over.
		slog.Error("name enquiry answered in an unrecognised shape", "sortCode", b.SortCode)
		writeJSON(w, http.StatusBadGateway,
			errBody(errors.New("the bank replied but we could not read the account name; this is our problem, not yours")))
		return
	case err != nil:
		slog.Error("name enquiry failed", "err", err)
		writeJSON(w, http.StatusBadGateway, errBody(errors.New("could not reach the bank")))
		return
	}

	// Fill in the bank's display name from the cached list, so the sender sees
	// "GTBANK PLC" rather than a sort code.
	if acct.BankName == "" {
		if banks, err := a.banks(r.Context()); err == nil {
			for _, bank := range banks {
				if bank.Code == acct.SortCode {
					acct.BankName = bank.Name
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, acct)
}

// limiter is a small fixed-window rate limiter keyed by user or address.
type limiter struct {
	mu     sync.Mutex
	seen   map[string][]time.Time
	limit  int
	window time.Duration
}

// NewLimiter builds a fixed-window limiter.
func NewLimiter(limit int, window time.Duration) *limiter {
	return &limiter{seen: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := time.Now().Add(-l.window)
	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.seen[key] = kept
		return false
	}
	l.seen[key] = append(kept, time.Now())

	// Keep the map from growing without bound in a long-running process.
	if len(l.seen) > 10000 {
		for k, v := range l.seen {
			if len(v) == 0 {
				delete(l.seen, k)
			}
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("Fly-Client-IP"); fwd != "" {
		return fwd
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return r.RemoteAddr
}

// ---------------------------------------------------------------- float

// floatStatus reconciles Tender's settlement capital three ways.
//
//	GL    the float control account
//	SL    the movements that compose it, by reason
//	BANK  what Fintava will actually let Tender pay out
//
// GL and SL are not an independent check and are not offered as one: the
// control balance is a database view over the very entries the detail sums, so
// they agree by construction. Saying otherwise would be inventing assurance.
// The detail earns its place by explaining the control figure, so a difference
// can be attributed rather than merely noticed.
//
// The check that can fail is GL against BANK. Only the bank figure limits a
// settlement, and a gap means money moved on the rail that the books never
// recorded -- better seen here than discovered by a payout failing.
func (a *API) floatStatus(w http.ResponseWriter, r *http.Request) {
	audit, err := a.Store.LedgerAudit(r.Context(), 1)
	if err != nil {
		fail(w, err)
		return
	}
	book, err := a.Store.FloatDetail(r.Context())
	if err != nil {
		fail(w, err)
		return
	}

	out := map[string]any{
		"currency": "NGN",
		"gl": map[string]any{
			"account":     "float",
			"controlKobo": audit.FloatKobo,
		},
		"sl": map[string]any{
			"lines":     book.Lines,
			"totalKobo": book.Total,
			"entries":   book.Entries,
			// True by construction, reported so the invariant is visible rather
			// than assumed. A false here would mean the view and the entries
			// have diverged, which should be impossible.
			"tiesToControl": book.Total == audit.FloatKobo,
		},
	}

	balance, err := a.Fintava.MerchantBalance(r.Context())
	switch {
	case errors.Is(err, fintava.ErrNotConfigured):
		out["bank"] = map[string]any{"available": false,
			"reason": "the bank rail is not configured on this deployment"}
		out["reconciled"] = false
		writeJSON(w, http.StatusOK, out)
		return
	case err != nil:
		// The books are still worth returning: what Tender believes it holds is
		// useful even when the rail cannot be reached to confirm it.
		slog.Error("merchant balance failed", "err", err)
		out["bank"] = map[string]any{"available": false,
			"reason": "could not read the balance held at the bank"}
		out["reconciled"] = false
		writeJSON(w, http.StatusOK, out)
		return
	}

	drift := balance - audit.FloatKobo
	out["bank"] = map[string]any{
		"available":   true,
		"source":      "fintava",
		"balanceKobo": balance,
	}
	out["driftKobo"] = drift
	out["reconciled"] = drift == 0 && book.Total == audit.FloatKobo
	writeJSON(w, http.StatusOK, out)
}

// fundFloat issues a one-time account that tops the float up.
//
// The amount is required and the account only accepts it, which is the point:
// a standing account that swallows any payment makes it impossible to say later
// which transfer a credit belonged to.
func (a *API) fundFloat(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AmountKobo   int64  `json:"amountKobo"`
		Reference    string `json:"reference"`
		ExpiresInMin int    `json:"expiresInMin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if b.AmountKobo <= 0 {
		writeJSON(w, http.StatusBadRequest,
			errBody(errors.New("say how much to add, in kobo")))
		return
	}
	ref := strings.TrimSpace(b.Reference)
	if ref == "" {
		ref = "float-" + uuid.NewString()
	}

	acct, err := a.Fintava.GenerateFundingAccount(
		r.Context(), money.Kobo(b.AmountKobo), ref, b.ExpiresInMin)
	switch {
	case errors.Is(err, fintava.ErrNotConfigured):
		writeJSON(w, http.StatusServiceUnavailable,
			errBody(errors.New("the bank rail is not configured on this deployment")))
		return
	case err != nil:
		slog.Error("float funding account failed", "err", err)
		writeJSON(w, http.StatusBadGateway,
			errBody(errors.New("could not get a funding account from the bank")))
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

// ---------------------------------------------------------------- webhook

// fintavaWebhook receives payout and funding events.
//
// The raw body is read before anything else touches it, because that is what
// the signature covers -- re-encoding parsed JSON would reorder keys and the
// HMAC would never match. Every event is recorded before it is acted on, and
// the provider's own reference is unique, so Fintava's 72 hours of retries
// cannot apply the same event twice.
func (a *API) fintavaWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	if err := a.Fintava.VerifyWebhook(raw, r.Header.Get(fintava.SignatureHeader)); err != nil {
		// Never say which part was wrong.
		slog.Warn("rejected webhook", "err", err, "ip", clientIP(r))
		writeJSON(w, http.StatusUnauthorized, errBody(errors.New("invalid signature")))
		return
	}

	event, err := fintava.ParseEvent(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	// Record first. A duplicate insert means this event has already been seen,
	// and the correct response is still 200 -- otherwise the provider keeps
	// retrying something that was handled.
	var eventID string
	err = a.Store.Pool.QueryRow(r.Context(), `
		INSERT INTO provider_events (event_type, provider_ref, payload)
		VALUES ($1, NULLIF($2,''), $3)
		ON CONFLICT (provider, event_type, provider_ref) DO NOTHING
		RETURNING id`, event.Type, event.Reference, raw).Scan(&eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		slog.Error("record webhook", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}

	// Acknowledge quickly; do the work without the provider's clock running.
	go a.applyEvent(event, eventID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) applyEvent(event fintava.Event, eventID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var applyErr error
	switch event.Type {
	case fintava.EventBankTransfer, fintava.EventTransferReversal:
		applyErr = a.applyPayoutEvent(ctx, event)
	case fintava.EventAccountFunded, fintava.EventWalletCredited:
		// Money arriving in the wallet that backs Tender's balances.
		applyErr = ledger.FundFloatFromBank(ctx, a.Store.Pool, event.Amount, event.Reference)
	default:
		slog.Info("ignoring webhook event", "type", event.Type)
	}

	note := ""
	if applyErr != nil {
		note = applyErr.Error()
		slog.Error("apply webhook", "type", event.Type, "ref", event.Reference, "err", applyErr)
	}
	_, _ = a.Store.Pool.Exec(ctx, `
		UPDATE provider_events SET processed_at = now(), error = NULLIF($2,'') WHERE id = $1`,
		eventID, note)
}

// applyPayoutEvent resolves the payout the event refers to. Matching is by the
// provider's own identifiers, both of which are unique in our table, so an
// event can only ever land on the payout it belongs to.
func (a *API) applyPayoutEvent(ctx context.Context, event fintava.Event) error {
	var id string
	err := a.Store.Pool.QueryRow(ctx, `
		SELECT id FROM payouts
		 WHERE (provider_ref = $1 AND $1 <> '')
		    OR (provider_tx_id = $2 AND $2 <> '')`, event.Reference, event.TxID).Scan(&id)
	if err != nil {
		return err
	}
	pid, err := uuid.Parse(id)
	if err != nil {
		return err
	}

	switch {
	case event.Type == fintava.EventTransferReversal || event.Failed():
		return a.Payouts.FailByID(ctx, pid, "provider reported "+event.Status)
	case event.Succeeded():
		return a.Payouts.Confirm(ctx, pid)
	}
	// An unrecognised status is left alone; reconciliation will settle it.
	return nil
}
