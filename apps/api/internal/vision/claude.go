package vision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"tender/api/internal/domain"
	"tender/api/internal/money"
)

// DefaultModel is the recognizer used unless VISION_MODEL says otherwise.
// Counting banknotes is a perception task rather than a reasoning one, so the
// model tier should be chosen by measurement -- see cmd/eval.
const DefaultModel = "claude-haiku-4-5"

// supportsEffort reports whether a model accepts output_config.effort.
//
// Sending it to a model that does not is a hard 400 -- "This model does not
// support the effort parameter" -- which took the whole pledge path down,
// because the default recognizer is Haiku 4.5 and Haiku 4.5 is one of the
// models that rejects it.
//
// The list is an allowlist rather than a denylist so the failure is safe in
// both directions. VISION_MODEL is operator-configurable, so an unrecognised
// value is possible; omitting effort from a model that would have accepted it
// costs a little token spend, while sending it to one that refuses stops
// people pledging cash.
func supportsEffort(model string) bool {
	for _, prefix := range []string{
		"claude-fable-5", "claude-mythos-5",
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-5", "claude-sonnet-4-6",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// MaxSerials caps how many serial numbers we ask for.
//
// Serials are a bonus, not the replay guard: the perceptual hash already
// catches a re-uploaded photograph. Asking for forty of them costs a great deal
// of output for very little extra protection, so we ask only for the ones that
// are clearly legible.
const MaxSerials = 12

// Claude reads banknotes out of a photograph.
type Claude struct {
	client anthropic.Client
	model  string
}

func NewClaude(apiKey, model string) *Claude {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if model == "" {
		model = DefaultModel
	}
	return &Claude{client: anthropic.NewClient(opts...), model: model}
}

func (c *Claude) Mode() string  { return "claude" }
func (c *Claude) Model() string { return c.model }

// The prompt deliberately never mentions what the sender declared.
//
// Telling the model the expected total would anchor it: asked to check a claim,
// a model tends to agree with it, and the one case that matters most is the one
// where the claim is wrong. It reports what it sees, and Go compares.
const visionSystem = `You are the note-recognition stage of a cash settlement system in Nigeria.

You are given a photograph that should show Nigerian naira banknotes laid out for counting.

Count the notes and report how many of each denomination you can see.

Counting method: work through the notes systematically -- left to right, top to bottom --
rather than estimating at a glance. If notes overlap so that you cannot tell how many are
underneath, do not guess: lower your confidence and say so in a warning.

Valid denominations are 1000, 500, 200, 100, 50, 20, 10 and 5 naira. Both the older
designs and the redesigned 200, 500 and 1000 notes are in circulation; treat them as the
same denomination. If you see something that is not one of these, do not report it as a
banknote.

Report only what you can actually observe. Never round a count to a convenient number and
never adjust it toward what a total "should" be -- an inaccurate count causes real
financial harm, and a disagreement is a useful signal rather than a problem to smooth over.

Also read up to ` + `%d` + ` serial numbers, choosing only those you can read clearly. Skip any that
are blurred, angled away, or covered. Reporting fewer is always better than guessing.

Also judge, and be conservative about both: these people are photographing their own
money in ordinary rooms, and a wrong accusation costs somebody a transfer they were
entitled to make.

  - screenReplay: is this a photograph of a screen displaying cash, rather than physical
    paper? Say true only for evidence that a screen leaves and paper cannot: a moire
    interference pattern, a visible pixel grid, or a device bezel around the image.
    Glare and bright hotspots are NOT evidence -- a phone flash on real banknotes on a
    table produces them constantly, and so does a polished surface. Neither is a dark or
    bluish cast. If the notes look like paper but the photograph is merely poor, that is
    low confidence, not a screen.
  - photocopySuspected: say true only for something you can actually see -- flat uniform
    ink with no tonal variation, obviously wrong paper colour, or a missing security
    thread on a note otherwise clear enough to check. Worn, creased, dirty and faded
    notes are normal currency in circulation and are not photocopies. If the image is
    too poor to judge, lower confidence instead.
  - confidence: how sure you are of the COUNT specifically, from 0 to 1. Overlapping notes,
    poor light, or motion blur should all lower it.

Respond with ONLY a JSON object, no prose and no markdown fence:
{"counts":{"1000":20,"500":4},"serials":["AB/12 3948217"],
 "screenReplay":false,"photocopySuspected":false,"confidence":0.9,"warnings":[]}

Use denomination values as the keys of "counts", in naira. Omit denominations you cannot
see rather than reporting them as zero.`

type claudeResponse struct {
	Counts             map[string]int `json:"counts"`
	Serials            []string       `json:"serials"`
	ScreenReplay       bool           `json:"screenReplay"`
	PhotocopySuspected bool           `json:"photocopySuspected"`
	Confidence         float64        `json:"confidence"`
	Warnings           []string       `json:"warnings"`
}

func (c *Claude) Analyze(ctx context.Context, raw []byte, _ money.Kobo) (*Result, error) {
	dhash, err := DHash(raw)
	if err != nil {
		return nil, err
	}

	mediaType := http.DetectContentType(raw)
	if !strings.HasPrefix(mediaType, "image/") {
		return nil, fmt.Errorf("not an image: %s", mediaType)
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 2048,
		System: []anthropic.TextBlockParam{
			{Text: fmt.Sprintf(visionSystem, MaxSerials)},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewImageBlockBase64(mediaType, base64.StdEncoding.EncodeToString(raw)),
				anthropic.NewTextBlock("Count the naira in this photograph and return the JSON object."),
			),
		},
	}
	// Counting is perception, not deliberation. Low effort keeps the snap
	// responsive, which matters more here than depth -- but only where the
	// model will accept being told.
	if supportsEffort(c.model) {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow}
	}

	msg, err := c.client.Messages.New(ctx, params)
	if err != nil {
		// The call never produced a reading. Whether that is billing, a bad key
		// or the network, the sender's photograph is not the problem.
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, c.model, err)
	}
	if msg.StopReason == "refusal" {
		return nil, fmt.Errorf("vision declined to read this image")
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return nil, fmt.Errorf("vision returned no reading for this image")
	}

	parsed, err := parseClaudeJSON(text.String())
	if err != nil {
		return nil, err
	}

	res := &Result{
		Mode:               "claude",
		Model:              c.model,
		ImageSHA256:        SHA256(raw),
		ImageDHash:         dhash,
		Confidence:         parsed.Confidence,
		ScreenReplay:       parsed.ScreenReplay,
		PhotocopySuspected: parsed.PhotocopySuspected,
		Warnings:           parsed.Warnings,
		InputTokens:        msg.Usage.InputTokens,
		OutputTokens:       msg.Usage.OutputTokens,
	}
	res.Notes, res.Warnings = expand(parsed, dhash, res.Warnings)
	for _, n := range res.Notes {
		res.Total += n.Denomination
	}
	return res, nil
}

// expand turns per-denomination counts back into individual notes, which is
// what the registry stores. Serials are attached to the first notes that have
// one; the rest are identified by the photograph's hash and their position.
func expand(p *claudeResponse, dhash string, warnings []string) ([]domain.Note, []string) {
	serials := make([]string, 0, len(p.Serials))
	seen := make(map[string]bool, len(p.Serials))
	for _, s := range p.Serials {
		n := normaliseSerial(s)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		serials = append(serials, n)
		if len(serials) >= MaxSerials {
			break
		}
	}

	// Largest denomination first, so the notes carrying serials are the ones
	// worth the most -- the ones a replay would be most profitable on.
	denoms := make([]int64, 0, len(p.Counts))
	for k := range p.Counts {
		var v int64
		if _, err := fmt.Sscanf(k, "%d", &v); err == nil {
			denoms = append(denoms, v)
		}
	}
	sort.Slice(denoms, func(i, j int) bool { return denoms[i] > denoms[j] })

	var notes []domain.Note
	idx := 0
	for _, d := range denoms {
		kobo := money.FromNaira(d)
		count := p.Counts[fmt.Sprintf("%d", d)]
		if !money.IsDenomination(kobo) {
			warnings = append(warnings,
				fmt.Sprintf("ignored %d reported ₦%d notes: not a real denomination", count, d))
			continue
		}
		if count <= 0 {
			continue
		}
		for i := 0; i < count; i++ {
			note := domain.Note{
				Denomination: kobo,
				PHash:        fmt.Sprintf("%s:%02d", dhash, idx),
			}
			if idx < len(serials) {
				note.Serial = serials[idx]
				note.SerialConfidence = 0.9
			}
			notes = append(notes, note)
			idx++
			if idx > 500 { // a photograph cannot plausibly hold more
				warnings = append(warnings, "stopped counting past 500 notes")
				return notes, warnings
			}
		}
	}
	return notes, warnings
}

// parseClaudeJSON tolerates a markdown fence around the object.
func parseClaudeJSON(s string) (*claudeResponse, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var out claudeResponse
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("vision returned unparseable JSON: %w", err)
	}
	return &out, nil
}

// normaliseSerial collapses spacing and case so two readings of the same note
// compare equal in the registry.
func normaliseSerial(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(strings.TrimSpace(s))), " ")
}
