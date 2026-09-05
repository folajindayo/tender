package settle

import (
	"testing"

	"tender/api/internal/domain"
	"tender/api/internal/money"
	"tender/api/internal/vision"
)

func twoFiveHundreds() []domain.Note {
	return []domain.Note{
		{Denomination: 50000}, {Denomination: 50000},
	}
}

// The failure that prompted this: a photograph the recognizer could not read
// came back as a fraud accusation, because the screen-replay verdict was ranked
// above "did we see anything". Somebody holding two real notes was told they
// were photographing a screen.
func TestUnreadablePhotoIsNotAnAccusation(t *testing.T) {
	blank := &vision.Result{
		Notes: nil, Total: 0, Confidence: 0,
		ScreenReplay: true, PhotocopySuspected: true,
	}
	got := screen(blank, money.Kobo(100000))
	if got == nil {
		t.Fatal("an empty reading must still be refused")
	}
	if got.Code != "no_notes" {
		t.Errorf("code = %q, want no_notes: a recognizer that saw nothing cannot "+
			"say what it was looking at", got.Code)
	}
}

// A poor but non-empty reading is a photography problem, not a fraud problem.
func TestLowConfidenceOutranksSuspicion(t *testing.T) {
	murky := &vision.Result{
		Notes: twoFiveHundreds(), Total: 100000, Confidence: 0.2,
		ScreenReplay: true,
	}
	got := screen(murky, money.Kobo(100000))
	if got == nil || got.Code != "low_confidence" {
		t.Fatalf("got %+v, want low_confidence", got)
	}
}

// The guard must not blunt real fraud detection: a screen replay of cash shows
// countable notes, which is the whole point of it.
func TestAConfidentScreenReplayIsStillRefused(t *testing.T) {
	replay := &vision.Result{
		Notes: twoFiveHundreds(), Total: 100000, Confidence: 0.95,
		ScreenReplay: true,
	}
	got := screen(replay, money.Kobo(100000))
	if got == nil || got.Code != "screen_replay" {
		t.Fatalf("got %+v, want screen_replay", got)
	}
}

func TestAConfidentPhotocopyIsStillRefused(t *testing.T) {
	copied := &vision.Result{
		Notes: twoFiveHundreds(), Total: 100000, Confidence: 0.95,
		PhotocopySuspected: true,
	}
	got := screen(copied, money.Kobo(100000))
	if got == nil || got.Code != "photocopy" {
		t.Fatalf("got %+v, want photocopy", got)
	}
}

// Two 500s for a 1,000 transfer is the ordinary case and must pass. Any mix
// adding to the declared amount is valid -- the denominations are the sender's
// business, not the system's.
func TestTwoFiveHundredsSettleAThousand(t *testing.T) {
	clean := &vision.Result{
		Notes: twoFiveHundreds(), Total: 100000, Confidence: 0.9,
	}
	if got := screen(clean, money.Kobo(100000)); got != nil {
		t.Fatalf("refused a valid pledge: %+v", got)
	}
}

func TestAMiscountIsReportedAsOne(t *testing.T) {
	short := &vision.Result{
		Notes: twoFiveHundreds(), Total: 50000, Confidence: 0.9,
	}
	got := screen(short, money.Kobo(100000))
	if got == nil || got.Code != "amount_mismatch" {
		t.Fatalf("got %+v, want amount_mismatch", got)
	}
}
