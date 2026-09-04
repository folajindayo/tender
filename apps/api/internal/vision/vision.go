// Package vision turns a photograph of cash into a structured claim.
//
// Nothing here secures value. Vision is a pre-filter: it catches wrong amounts,
// screen replays and photocopies so that people do not waste a trip, and it
// produces per-note identifiers so the same notes cannot be pledged twice while
// a transfer is open. Authenticity is established by the human counterparty who
// physically receives the notes, not by this package.
package vision

import (
	"context"

	"tender/api/internal/domain"
	"tender/api/internal/money"
)

// Result is what the recognizer made of one photograph.
type Result struct {
	Notes              []domain.Note `json:"notes"`
	Total              money.Kobo    `json:"totalKobo"`
	Confidence         float64       `json:"confidence"`
	ScreenReplay       bool          `json:"screenReplay"`
	PhotocopySuspected bool          `json:"photocopySuspected"`
	Warnings           []string      `json:"warnings"`
	Mode               string        `json:"mode"`
	Model              string        `json:"model,omitempty"`
	ImageSHA256        string        `json:"imageSha256"`
	ImageDHash         string        `json:"imageDhash"`

	// Token usage for the call that produced this reading, so model choice can
	// be settled by measurement rather than estimate. Zero for the stub.
	InputTokens  int64 `json:"inputTokens,omitempty"`
	OutputTokens int64 `json:"outputTokens,omitempty"`
}

// Provider analyses a photograph of banknotes.
type Provider interface {
	// Analyze inspects raw image bytes. declared is what the sender said they
	// were pledging, passed only so the provider can flag a mismatch -- it must
	// never be used to fabricate agreement.
	Analyze(ctx context.Context, raw []byte, declared money.Kobo) (*Result, error)
	Mode() string
}
