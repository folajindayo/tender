package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"tender/api/internal/domain"
)

// Sessions last a month. Long enough that a demo phone stays signed in, short
// enough that a stolen token is not permanent.
const sessionTTL = 30 * 24 * time.Hour

const sessionCookie = "tender_session"

// ---------------------------------------------------------------- signup

func (a *API) signup(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
		Phone       string `json:"phone"`
		City        string `json:"city"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	email := strings.ToLower(strings.TrimSpace(b.Email))
	if _, err := mail.ParseAddress(email); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("that does not look like an email address")))
		return
	}
	// Long minimum rather than a character-class rule: length is what actually
	// resists guessing, and composition rules mostly produce Password1!.
	if len(b.Password) < 8 {
		writeJSON(w, http.StatusBadRequest,
			errBody(errors.New("use a password of at least 8 characters")))
		return
	}
	name := strings.TrimSpace(b.DisplayName)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}

	// A phone number is still the account's public handle, but nobody is asked
	// to verify one to sign up. When it is absent the email stands in, so the
	// NOT NULL column is satisfied without inventing a plausible-looking number.
	phone := strings.TrimSpace(b.Phone)
	if phone == "" {
		phone = email
	}

	// city is NOT NULL, so an unanswered field is an empty string rather than a
	// null -- the app asks for a city but does not require one.
	city := strings.TrimSpace(b.City)

	var id uuid.UUID
	err = a.Store.Pool.QueryRow(r.Context(), `
		INSERT INTO users (phone, display_name, email, password_hash, city, avatar_emoji)
		VALUES ($1,$2,$3,$4,$5,'🙂')
		RETURNING id`, phone, name, email, string(hash), city).Scan(&id)
	if err != nil {
		if isDuplicate(err) {
			// Same wording whichever field collided: telling someone which
			// addresses are already registered is an account-enumeration tool.
			writeJSON(w, http.StatusConflict,
				errBody(errors.New("an account with those details already exists")))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}

	a.startSession(w, r, id)
}

// ---------------------------------------------------------------- signin

func (a *API) signin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))

	// Rate limited per address and per source, because an unmetered signin
	// endpoint is a password-guessing endpoint.
	if !a.Signins.allow(email) || !a.Signins.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests,
			errBody(errors.New("too many attempts; wait a minute and try again")))
		return
	}

	var id uuid.UUID
	var hash string
	err := a.Store.Pool.QueryRow(r.Context(), `
		SELECT id, password_hash FROM users
		 WHERE lower(email) = $1 AND password_hash IS NOT NULL`, email).Scan(&id, &hash)

	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the same work as a real check would, so response time does not
		// reveal whether the address exists.
		bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"),
			[]byte(b.Password))
		writeJSON(w, http.StatusUnauthorized, errBody(errors.New("wrong email or password")))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, errBody(errors.New("wrong email or password")))
		return
	}

	a.startSession(w, r, id)
}

// ---------------------------------------------------------------- session

// startSession issues a token, stores only its hash, and returns it both as a
// cookie and in the body.
//
// Both, because the PWA and the API are on different origins: a cookie needs
// SameSite=None and third-party cookies, which some browsers refuse outright,
// so the token is also returned for clients that hold it themselves.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(sessionTTL)

	if _, err := a.Store.Pool.Exec(r.Context(), `
		INSERT INTO sessions (user_id, token_hash, user_agent, expires_at)
		VALUES ($1,$2,NULLIF($3,''),$4)`,
		userID, hashToken(token), r.UserAgent(), expires); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})

	user, err := a.Store.GetUser(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}

func (a *API) signout(w http.ResponseWriter, r *http.Request) {
	if token := bearer(r); token != "" {
		if _, err := a.Store.Pool.Exec(r.Context(),
			`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			hashToken(token)); err != nil {
			slog.Error("revoke session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// me returns the signed-in user, and is how the PWA decides whether to show the
// app or the sign-in screen on load.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFor(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody(errors.New("not signed in")))
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// userFor resolves the caller from their session token.
func (a *API) userFor(r *http.Request) (*domain.User, error) {
	token := bearer(r)
	if token == "" {
		return nil, errors.New("no session")
	}
	var userID uuid.UUID
	err := a.Store.Pool.QueryRow(r.Context(), `
		SELECT user_id FROM sessions
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
		hashToken(token)).Scan(&userID)
	if err != nil {
		return nil, err
	}
	return a.Store.GetUser(r.Context(), userID)
}

// bearer reads the token from the Authorization header first, then the cookie.
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// hashToken is SHA-256 rather than bcrypt on purpose: the token is 32 bytes of
// entropy from a CSPRNG, so there is nothing to brute force, and this runs on
// every authenticated request.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// isDuplicate matches on the driver's structured error code rather than the
// text of the message, which varies with the Postgres locale and version.
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Revoked rows are kept for a while because "when did this session end" is a
// question worth being able to answer.
// SweepSessions deletes what has expired.
func (a *API) SweepSessions(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := a.Store.Pool.Exec(ctx,
				`DELETE FROM sessions WHERE expires_at < now() - interval '30 days'`); err != nil {
				slog.Error("sweep sessions", "err", err)
			}
		}
	}
}
