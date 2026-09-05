// Package fintava talks to Fintava, the bank rail behind Tender.
//
// Two things depend on it. Name enquiry turns an account number into the name
// on the account, which is what a sender checks before parting with cash. Bank
// credit moves money out of Tender's wallet into somebody's bank account, which
// is what settlement actually does now.
//
// The published reference documents request shapes but leaves most response
// bodies as an empty object, so responses here are decoded tolerantly: the
// envelope is read, and fields are looked up under each name the provider might
// plausibly use. That is deliberate. Guessing one shape and hard-coding it
// would fail at the first live call with an error that looked like a network
// fault instead of a parsing one.
package fintava

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"tender/api/internal/money"
)

// DefaultBaseURL is the sandbox. Production is set through configuration; there
// is no default for it, so a missing FINTAVA_BASE_URL can never silently move
// real money.
const DefaultBaseURL = "https://dev.fintavapay.com/api/dev"

var (
	// ErrUnreadable means the provider answered successfully but the body held
	// no account name under any key we know. That is not the same as the
	// account not existing, and collapsing the two would let a decoding bug
	// masquerade as a mistyped account number indefinitely -- the published
	// reference documents this response as an empty object, so the key names
	// here are inferred and could be wrong.
	ErrUnreadable = errors.New("fintava: name enquiry answered in an unrecognised shape")

	// ErrNotConfigured means no API key was supplied. Callers surface this as
	// "bank transfers are unavailable" rather than pretending a payout worked.
	ErrNotConfigured = errors.New("fintava: not configured")

	// ErrAccountNotFound is a name enquiry the bank could not resolve.
	ErrAccountNotFound = errors.New("fintava: account not found")

	// ErrIndeterminate is the important one. The request left this process but
	// no answer came back, so the money may or may not have moved. It must
	// never be retried blindly -- only reconciled against the provider.
	ErrIndeterminate = errors.New("fintava: outcome unknown")
)

type Config struct {
	BaseURL string
	APIKey  string

	// SourceID is the Fintava customer whose wallet is debited: Tender's float.
	SourceID string

	// WebhookSecret verifies inbound events. See webhook.go.
	WebhookSecret string

	Timeout time.Duration
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout == 0 {
		// Long enough for a bank rail on a bad day, short enough that a stuck
		// request does not hold a settlement transaction open indefinitely.
		cfg.Timeout = 30 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// Configured reports whether real calls can be made. The API refuses to accept
// bank recipients when this is false, rather than accepting transfers it has no
// way to complete.
func (c *Client) Configured() bool { return c != nil && c.cfg.APIKey != "" }

// CanPayOut reports whether payouts can be initiated. Payouts debit the float
// (via the merchant wallet or a resolved source wallet).
func (c *Client) CanPayOut() bool { return c.Configured() }

// SourceID is the Fintava customer whose wallet funds payouts (optional).
func (c *Client) SourceID() string { return c.cfg.SourceID }

// ---------------------------------------------------------------- envelope

// envelope is the shape every documented Fintava response shares.
type envelope struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type APIError struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fintava: %s (http %d)", e.Message, e.StatusCode)
	}
	return fmt.Sprintf("fintava: http %d: %s", e.StatusCode, truncate(e.Body, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var env envelope
	// A body that is not JSON at all is still worth reporting verbatim: it is
	// usually a gateway error page, and the text says which gateway.
	_ = json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: env.Message, Body: string(raw)}
	}
	if env.Data == nil {
		// Some endpoints return the payload at the top level rather than under
		// "data". Fall back to the whole body instead of failing.
		return raw, nil
	}
	return env.Data, nil
}

// ---------------------------------------------------------------- banks

type Bank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Banks lists the institutions Fintava can reach, with the sort codes every
// other call needs.
func (c *Client) Banks(ctx context.Context) ([]Bank, error) {
	data, err := c.do(ctx, http.MethodGet, "/banks?order=ASC", nil)
	if err != nil {
		return nil, err
	}
	var banks []Bank
	if err := json.Unmarshal(data, &banks); err != nil {
		return nil, fmt.Errorf("decode banks: %w", err)
	}
	return banks, nil
}

// ---------------------------------------------------------------- name enquiry

type Account struct {
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	SortCode      string `json:"sortCode"`
	BankName      string `json:"bankName,omitempty"`
}

// ResolveAccount returns the name a bank holds for an account number.
//
// This is the check that makes a typed account number safe: the sender reads
// the name back before any cash changes hands. It is also why the endpoint in
// front of it is rate limited -- unmetered name enquiry is an easy way to
// harvest the name behind any account number in the country.
func (c *Client) ResolveAccount(ctx context.Context, accountNumber, sortCode string) (Account, error) {
	q := url.Values{"accountNumber": {accountNumber}, "sortCode": {sortCode}}
	data, err := c.do(ctx, http.MethodGet, "/name/enquiry?"+q.Encode(), nil)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			return Account{}, ErrAccountNotFound
		}
		return Account{}, err
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return Account{}, fmt.Errorf("decode name enquiry: %w", err)
	}

	acct := Account{
		AccountName: pickString(fields,
			"accountName", "account_name", "name", "beneficiaryName", "accountname",
			"AccountName", "acctName", "account_holder_name", "accountHolderName",
			"customerName", "fullName", "beneficiary_name"),
		AccountNumber: pickString(fields, "accountNumber", "account_number", "accountNo", "acctNo"),
		SortCode:      pickString(fields, "sortCode", "sort_code", "bankCode", "code"),
		BankName:      pickString(fields, "bankName", "bank_name", "bank"),
	}
	if acct.AccountName == "" {
		// Report the shape, never the values: this body is somebody's banking
		// detail. The key names are enough to fix the decoder and carry no PII.
		slog.Warn("name enquiry returned no readable account name",
			"keys", shapeOf(fields), "sortCode", sortCode)
		return Account{}, ErrUnreadable
	}
	if acct.AccountNumber == "" {
		acct.AccountNumber = accountNumber
	}
	if acct.SortCode == "" {
		acct.SortCode = sortCode
	}
	return acct, nil
}

// ---------------------------------------------------------------- payout

type BankCreditRequest struct {
	AccountNumber string      `json:"accountNumber"`
	AccountName   string      `json:"accountName"`
	SortCode      string      `json:"sortCode"`
	Amount        json.Number `json:"amount"`
	SourceID      string      `json:"sourceId"`
	Narration     string      `json:"narration,omitempty"`
}

type Payout struct {
	Reference         string     `json:"reference"`
	CustomerReference string     `json:"customerReference"`
	ID                string     `json:"id"`
	Amount            money.Kobo `json:"amountKobo"`
	FeeKobo           money.Kobo `json:"feeKobo"`
	Status            string     `json:"status"`
}

// BankCredit moves money out of Tender's wallet into a bank account.
//
// Fintava exposes no idempotency key, so this call cannot be made safe by
// repeating it. Safety lives on our side instead: one payout row per transfer,
// enforced by a unique constraint, and a state machine that only ever calls
// this from 'pending'. A transport failure returns ErrIndeterminate and the
// payout goes to 'unknown', where reconciliation -- not a retry -- decides what
// happened.
func (c *Client) BankCredit(ctx context.Context, req BankCreditRequest) (Payout, error) {
	if !c.CanPayOut() {
		return Payout{}, ErrNotConfigured
	}
	path := "/bank/credit"
	if req.SourceID == "" {
		// When no customer sourceId is specified, payout directly from the merchant float wallet
		path = "/bank/credit/merchant"
	}
	data, err := c.do(ctx, http.MethodPost, path, req)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// The provider answered and refused. Nothing moved.
			return Payout{}, err
		}
		// No answer came back. This is the dangerous case.
		return Payout{}, fmt.Errorf("%w: %v", ErrIndeterminate, err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		// The provider accepted it but we cannot read the receipt. Treat it as
		// indeterminate rather than failed: money may well have moved.
		return Payout{}, fmt.Errorf("%w: decode payout: %v", ErrIndeterminate, err)
	}

	return Payout{
		Reference:         pickString(fields, "reference", "transactionReference", "ref"),
		CustomerReference: pickString(fields, "customerReference", "customer_reference"),
		ID:                pickString(fields, "id", "transactionId", "transaction_id"),
		Amount:            pickKobo(fields, "amount"),
		FeeKobo:           pickKobo(fields, "transaction_fee", "transactionFee", "charges", "fee"),
		Status:            strings.ToUpper(pickString(fields, "status", "transactionStatus")),
	}, nil
}

// ---------------------------------------------------------------- reconcile

type Transaction struct {
	ID        string     `json:"id"`
	Reference string     `json:"reference"`
	Status    string     `json:"status"`
	Amount    money.Kobo `json:"amountKobo"`
}

// Succeeded, Failed and Pending read the provider's status vocabulary. Anything
// unrecognised is treated as still pending, so an unfamiliar status can never
// be mistaken for a completed payout.
func (t Transaction) Succeeded() bool { return isSuccess(t.Status) }
func (t Transaction) Failed() bool    { return isFailure(t.Status) }
func (t Transaction) Pending() bool   { return !t.Succeeded() && !t.Failed() }

func isSuccess(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS", "SUCCESSFUL", "COMPLETED", "COMPLETE", "PAID", "SETTLED":
		return true
	}
	return false
}

func isFailure(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FAILED", "FAILURE", "REJECTED", "DECLINED", "REVERSED", "CANCELLED", "CANCELED":
		return true
	}
	return false
}

// Transaction looks up one transaction by the provider's id. This is how an
// 'unknown' payout is resolved: ask what actually happened rather than send it
// again.
func (c *Client) Transaction(ctx context.Context, id string) (Transaction, error) {
	data, err := c.do(ctx, http.MethodGet, "/transaction/id/"+url.PathEscape(id), nil)
	if err != nil {
		return Transaction{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return Transaction{}, fmt.Errorf("decode transaction: %w", err)
	}
	return Transaction{
		ID:        pickString(fields, "id", "transactionId"),
		Reference: pickString(fields, "reference", "transactionReference", "ref"),
		Status:    strings.ToUpper(pickString(fields, "status", "transactionStatus", "state")),
		Amount:    pickKobo(fields, "amount"),
	}, nil
}

// ---------------------------------------------------------------- helpers

// Naira renders integer kobo as the decimal the API expects, without ever
// putting money through a float.
func Naira(k money.Kobo) json.Number {
	sign := ""
	if k < 0 {
		sign, k = "-", -k
	}
	return json.Number(fmt.Sprintf("%s%d.%02d", sign, int64(k)/100, int64(k)%100))
}

// pickString returns the first key that is present and non-empty. Nested "data"
// objects are searched too, since some responses wrap the payload twice.
// shapeOf lists the keys of a response, one level deep, so an unrecognised body
// can be diagnosed from a log line without ever recording what it contained.
func shapeOf(fields map[string]any) []string {
	out := make([]string, 0, len(fields))
	for k, v := range fields {
		if nested, ok := v.(map[string]any); ok {
			for nk := range nested {
				out = append(out, k+"."+nk)
			}
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pickString finds the first of `keys` present anywhere in the response.
//
// It searches the whole object, not just a wrapper named "data". Name enquiry
// returns {status, account:{accountName, ...}}, and a decoder that only ever
// looked inside "data" reported every real account as missing. Which wrapper a
// provider uses is not knowable from a reference that documents its responses
// as empty objects, so the search does not depend on guessing one.
//
// The top level is searched first, then nested objects, so an outer field wins
// over one buried in a sub-object.
func pickString(fields map[string]any, keys ...string) string {
	return search(fields, func(v any) (string, bool) {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s, true
			}
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), true
		case json.Number:
			return t.String(), true
		}
		return "", false
	}, keys)
}

// search walks a decoded JSON object breadth-first, returning the first value
// under any of `keys` that `take` accepts.
func search[T any](fields map[string]any, take func(any) (T, bool), keys []string) T {
	var zero T
	queue := []map[string]any{fields}
	// A response is a handful of small objects; the bound is only here so a
	// pathological body cannot spin.
	for depth := 0; len(queue) > 0 && depth < 64; depth++ {
		level := queue
		queue = nil
		for _, obj := range level {
			for _, k := range keys {
				if got, ok := take(obj[k]); ok {
					return got
				}
			}
			for _, v := range obj {
				if nested, ok := v.(map[string]any); ok {
					queue = append(queue, nested)
				}
			}
		}
	}
	return zero
}

// pickKobo reads a naira amount and converts it to integer kobo. The provider
// sends JSON numbers, so this rounds at the last step rather than carrying a
// float any further than it has to.
func pickKobo(fields map[string]any, keys ...string) money.Kobo {
	return search(fields, func(v any) (money.Kobo, bool) {
		switch t := v.(type) {
		case float64:
			return money.Kobo(int64(t*100 + 0.5)), true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
				return money.Kobo(int64(f*100 + 0.5)), true
			}
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return money.Kobo(int64(f*100 + 0.5)), true
			}
		}
		return 0, false
	}, keys)
}
