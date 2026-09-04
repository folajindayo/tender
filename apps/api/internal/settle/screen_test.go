package settle

import (
	"testing"

	"tender/api/internal/domain"
	"tender/api/internal/money"
	"tender/api/internal/vision"
)

func notes(n int, denom money.Kobo) []domain.Note {
	out := make([]domain.Note, n)
	for i := range out {
		out[i] = domain.Note{Denomination: denom}
	}
	return out
}

func TestScreen(t *testing.T) {
	twentyK := money.FromNaira(20000)
	good := func() *vision.Result {
		return &vision.Result{
			Notes:      notes(40, money.FromNaira(500)),
			Total:      twentyK,
			Confidence: 0.9,
		}
	}

	t.Run("a clean count passes", func(t *testing.T) {
		if r := screen(good(), twentyK); r != nil {
			t.Errorf("expected acceptance, got %q", r.Reason)
		}
	})

	t.Run("a screen replay is refused", func(t *testing.T) {
		v := good()
		v.ScreenReplay = true
		r := screen(v, twentyK)
		if r == nil || r.Code != "screen_replay" {
			t.Errorf("expected screen_replay, got %+v", r)
		}
	})

	t.Run("suspected photocopies are refused", func(t *testing.T) {
		v := good()
		v.PhotocopySuspected = true
		if r := screen(v, twentyK); r == nil || r.Code != "photocopy" {
			t.Errorf("expected photocopy, got %+v", r)
		}
	})

	t.Run("an empty photograph is refused", func(t *testing.T) {
		v := good()
		v.Notes = nil
		if r := screen(v, twentyK); r == nil || r.Code != "no_notes" {
			t.Errorf("expected no_notes, got %+v", r)
		}
	})

	// Overstating is the fraud case; understating is a miscount. Both are
	// refused, because a counterparty would reject the handover either way.
	t.Run("a short count is refused", func(t *testing.T) {
		v := good()
		v.Total = money.FromNaira(19000)
		if r := screen(v, twentyK); r == nil || r.Code != "amount_mismatch" {
			t.Errorf("expected amount_mismatch, got %+v", r)
		}
	})

	t.Run("an over count is refused", func(t *testing.T) {
		v := good()
		v.Total = money.FromNaira(21000)
		if r := screen(v, twentyK); r == nil || r.Code != "amount_mismatch" {
			t.Errorf("expected amount_mismatch, got %+v", r)
		}
	})

	t.Run("an unclear photograph is refused", func(t *testing.T) {
		v := good()
		v.Confidence = 0.2
		if r := screen(v, twentyK); r == nil || r.Code != "low_confidence" {
			t.Errorf("expected low_confidence, got %+v", r)
		}
	})
}
