// Command eval measures how well a model actually counts naira.
//
// Model choice for this task should be settled by measurement, not by intuition
// about model tiers: counting banknotes is perception, and a cheaper model may
// well do it as accurately as an expensive one at a twentieth of the price. This
// runs a folder of hand-labelled photographs through one or more models and
// reports accuracy, serial yield, fraud-signal behaviour, latency and cost.
//
//	go run ./cmd/eval -manifest testdata/naira/manifest.json \
//	    -models claude-haiku-4-5,claude-sonnet-5,claude-opus-5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tender/api/internal/money"
	"tender/api/internal/vision"
)

// Case is one hand-labelled photograph. Counts are keyed by denomination in
// naira, so {"1000": 20} means twenty ₦1,000 notes.
type Case struct {
	File         string         `json:"file"`
	Counts       map[string]int `json:"counts"`
	ScreenReplay bool           `json:"screenReplay"`
	Photocopy    bool           `json:"photocopy"`
	Note         string         `json:"note,omitempty"`
}

type Manifest struct {
	Cases []Case `json:"images"`
}

// pricing is USD per million tokens, input and output.
var pricing = map[string][2]float64{
	"claude-haiku-4-5": {1, 5},
	"claude-sonnet-5":  {2, 10},
	"claude-opus-5":    {5, 25},
	"claude-fable-5-1": {10, 50},
}

// nairaPerUSD is only used to express cost per snap in the currency the fee is
// charged in. Override when the rate moves.
var nairaPerUSD = flag.Float64("ngn", 1600, "naira per US dollar, for cost reporting")

func main() {
	manifestPath := flag.String("manifest", "testdata/naira/manifest.json", "labelled image manifest")
	models := flag.String("models", vision.DefaultModel, "comma-separated models to compare")
	feeBPS := flag.Int("fee-bps", 50, "platform fee in basis points, for the margin column")
	flag.Parse()

	manifest, err := load(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "\nCreate a manifest like:")
		fmt.Fprintln(os.Stderr, sampleManifest)
		os.Exit(1)
	}
	if len(manifest.Cases) == 0 {
		fmt.Fprintln(os.Stderr, "the manifest has no images in it")
		os.Exit(1)
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(1)
	}

	dir := filepath.Dir(*manifestPath)
	ctx := context.Background()

	var reports []Report
	for _, model := range strings.Split(*models, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		fmt.Printf("\n\033[1m%s\033[0m  (%d images)\n", model, len(manifest.Cases))
		reports = append(reports, run(ctx, model, dir, manifest.Cases))
	}

	fmt.Printf("\n\033[1mSummary\033[0m\n")
	summarise(reports, *feeBPS)
}

// Report is what one model scored across the whole set.
type Report struct {
	Model string
	N     int

	ExactTotal   int // the detected total matched the label exactly
	CountedOver  int
	CountedUnder int
	Failed       int // the call errored or returned nothing usable

	// Absolute error in notes, summed across denominations, per image.
	NoteErrors []int

	NotesLabelled int
	SerialsRead   int

	ScreenTP, ScreenFP, ScreenFN int
	PhotoTP, PhotoFP             int

	Latencies []time.Duration
	InTokens  int64
	OutTokens int64
}

func run(ctx context.Context, model, dir string, cases []Case) Report {
	provider := vision.NewClaude(os.Getenv("ANTHROPIC_API_KEY"), model)
	r := Report{Model: model, N: len(cases)}

	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join(dir, c.File))
		if err != nil {
			fmt.Printf("  %-28s \033[31mcannot read: %v\033[0m\n", c.File, err)
			r.Failed++
			continue
		}

		wantNotes, wantTotal := expected(c)
		r.NotesLabelled += wantNotes

		start := time.Now()
		res, err := provider.Analyze(ctx, raw, wantTotal)
		took := time.Since(start)
		if err != nil {
			fmt.Printf("  %-28s \033[31m%v\033[0m\n", c.File, err)
			r.Failed++
			continue
		}
		r.Latencies = append(r.Latencies, took)
		r.InTokens += res.InputTokens
		r.OutTokens += res.OutputTokens

		gotCounts := countByDenomination(res)
		noteErr := countError(c.Counts, gotCounts)
		r.NoteErrors = append(r.NoteErrors, noteErr)

		for _, n := range res.Notes {
			if n.Serial != "" {
				r.SerialsRead++
			}
		}

		switch {
		case res.Total == wantTotal:
			r.ExactTotal++
		case res.Total > wantTotal:
			r.CountedOver++
		default:
			r.CountedUnder++
		}

		switch {
		case c.ScreenReplay && res.ScreenReplay:
			r.ScreenTP++
		case c.ScreenReplay && !res.ScreenReplay:
			r.ScreenFN++
		case !c.ScreenReplay && res.ScreenReplay:
			r.ScreenFP++
		}
		if c.Photocopy && res.PhotocopySuspected {
			r.PhotoTP++
		}
		if !c.Photocopy && res.PhotocopySuspected {
			r.PhotoFP++
		}

		mark, colour := "✓", "32"
		if res.Total != wantTotal {
			mark, colour = "✗", "31"
		}
		fmt.Printf("  \033[%sm%s\033[0m %-28s want %-12s got %-12s notes±%d  %s  %.0fms\n",
			colour, mark, c.File, wantTotal, res.Total, noteErr,
			fmt.Sprintf("conf %.2f", res.Confidence), float64(took.Milliseconds()))
	}
	return r
}

func expected(c Case) (notes int, total money.Kobo) {
	for k, v := range c.Counts {
		d, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			continue
		}
		notes += v
		total += money.FromNaira(d) * money.Kobo(v)
	}
	return notes, total
}

func countByDenomination(res *vision.Result) map[string]int {
	out := map[string]int{}
	for _, n := range res.Notes {
		out[strconv.FormatInt(int64(n.Denomination/100), 10)]++
	}
	return out
}

// countError is the total number of notes miscounted, in either direction.
func countError(want, got map[string]int) int {
	seen := map[string]bool{}
	total := 0
	for k, v := range want {
		seen[k] = true
		total += abs(v - got[k])
	}
	for k, v := range got {
		if !seen[k] {
			total += v
		}
	}
	return total
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func summarise(reports []Report, feeBPS int) {
	fmt.Printf("\n  %-18s %8s %8s %9s %8s %9s %10s %12s\n",
		"model", "exact", "note err", "serials", "screen", "p50", "cost/snap", "fee margin")
	fmt.Println("  " + strings.Repeat("─", 88))

	for _, r := range reports {
		scored := r.N - r.Failed
		if scored == 0 {
			fmt.Printf("  %-18s  all %d calls failed\n", r.Model, r.N)
			continue
		}

		exact := pct(r.ExactTotal, scored)
		meanErr := mean(r.NoteErrors)
		serials := pct(r.SerialsRead, r.NotesLabelled)

		screen := "n/a"
		if r.ScreenTP+r.ScreenFN > 0 {
			screen = fmt.Sprintf("%d/%d", r.ScreenTP, r.ScreenTP+r.ScreenFN)
		}
		if r.ScreenFP > 0 {
			screen += fmt.Sprintf(" +%dfp", r.ScreenFP)
		}

		cost := costPerCall(r, scored)
		// A ₦20,000 transfer is the reference: the fee has to cover the snap.
		fee := float64(money.FeeFor(money.FromNaira(20000), feeBPS)) / 100 / *nairaPerUSD
		margin := "—"
		if fee > 0 {
			margin = fmt.Sprintf("%.0f%%", (fee-cost)/fee*100)
		}

		fmt.Printf("  %-18s %7.0f%% %8.1f %8.0f%% %9s %8.0fms %9s %12s\n",
			r.Model, exact, meanErr, serials, screen,
			float64(p50(r.Latencies).Milliseconds()),
			fmt.Sprintf("₦%.1f", cost**nairaPerUSD), margin)
	}

	fmt.Println("\n  exact       the detected total matched the label exactly — this is your pass rate")
	fmt.Println("  note err    mean notes miscounted per photograph, in either direction")
	fmt.Println("  serials     share of notes whose serial was legible")
	fmt.Println("  screen      screen replays caught / screen replays present (+false positives)")
	fmt.Println("  fee margin  what is left of the fee on a ₦20,000 transfer after paying for the snap")
	fmt.Println()
}

func costPerCall(r Report, scored int) float64 {
	price, ok := pricing[r.Model]
	if !ok {
		return 0
	}
	in := float64(r.InTokens) / 1e6 * price[0]
	out := float64(r.OutTokens) / 1e6 * price[1]
	return (in + out) / float64(scored)
}

func pct(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) / float64(of) * 100
}

func mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

func p50(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

const sampleManifest = `{
  "images": [
    {"file": "market-01.jpg", "counts": {"1000": 20}},
    {"file": "mixed-02.jpg",  "counts": {"500": 8, "200": 5, "100": 3}},
    {"file": "screen-01.jpg", "counts": {}, "screenReplay": true, "note": "photo of a laptop screen"}
  ]
}`
