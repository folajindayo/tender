package fintava

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"tender/api/internal/money"
)

// SignatureHeader carries the HMAC of the raw request body.
const SignatureHeader = "x-fintava-signature"

var (
	ErrBadSignature    = errors.New("fintava: webhook signature mismatch")
	ErrNoWebhookSecret = errors.New("fintava: no webhook secret configured")
)

// Event names Fintava sends. Only the ones Tender acts on are named here.
const (
	// EventBankTransfer reports the outcome of a payout we initiated.
	EventBankTransfer = "customer_bank_transfer"
	// EventTransferReversal reports a payout that was undone after the fact.
	EventTransferReversal = "debit_transfer_reversal"
	// EventAccountFunded reports money arriving in the float wallet.
	EventAccountFunded = "account_funded"
	// EventWalletCredited reports an internal credit or a Fintava reversal.
	EventWalletCredited = "customer_wallet_credited"
)

// VerifyWebhook checks the signature over the exact bytes received.
//
// The raw body is what is signed, so the caller must hand over the bytes it
// read off the wire -- not a re-encoding of the parsed JSON. Re-marshalling
// reorders keys and changes whitespace, and the signature would never match.
// The comparison is constant time so a wrong signature leaks nothing about how
// wrong it was.
func (c *Client) VerifyWebhook(rawBody []byte, signature string) error {
	if c == nil || c.cfg.WebhookSecret == "" {
		return ErrNoWebhookSecret
	}
	mac := hmac.New(sha512.New, []byte(c.cfg.WebhookSecret))
	mac.Write(rawBody)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(strings.TrimSpace(strings.ToLower(signature))), []byte(expected)) {
		return ErrBadSignature
	}
	return nil
}

// Event is the part of a webhook payload Tender cares about, pulled out of a
// body whose exact shape varies by event type.
type Event struct {
	Type      string         `json:"event"`
	Reference string         `json:"reference"`
	TxID      string         `json:"id"`
	Status    string         `json:"status"`
	Amount    money.Kobo     `json:"amountKobo"`
	Raw       map[string]any `json:"-"`
}

func (e Event) Succeeded() bool { return isSuccess(e.Status) }
func (e Event) Failed() bool    { return isFailure(e.Status) }

// ParseEvent reads an event without assuming a single payload shape. Fintava
// documents different field sets per event, and nests the useful ones under
// "data" for some and not others, so every lookup is tolerant.
func ParseEvent(rawBody []byte) (Event, error) {
	var fields map[string]any
	if err := json.Unmarshal(rawBody, &fields); err != nil {
		return Event{}, err
	}
	return Event{
		Type:      pickString(fields, "event", "eventType", "event_type", "type"),
		Reference: pickString(fields, "reference", "transactionReference", "transaction_reference", "reversalRef", "ref"),
		TxID:      pickString(fields, "id", "transactionId", "transaction_id"),
		Status:    strings.ToUpper(pickString(fields, "status", "transactionStatus")),
		Amount:    pickKobo(fields, "amount"),
		Raw:       fields,
	}, nil
}
