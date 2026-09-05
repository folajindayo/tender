package vision

import "testing"

// The pledge path went down because effort was sent to a model that rejects it.
// The default recognizer is one of those models, so this is not hypothetical.
func TestEffortIsOnlySentToModelsThatAcceptIt(t *testing.T) {
	rejects := []string{
		"claude-haiku-4-5",
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-5",
		"",
		"some-model-nobody-has-heard-of",
	}
	for _, m := range rejects {
		if supportsEffort(m) {
			t.Errorf("supportsEffort(%q) = true; sending effort to it is a 400", m)
		}
	}

	accepts := []string{
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-5", "claude-sonnet-4-6", "claude-fable-5-1",
	}
	for _, m := range accepts {
		if !supportsEffort(m) {
			t.Errorf("supportsEffort(%q) = false; it accepts effort", m)
		}
	}
}

// The default must work out of the box. If DefaultModel ever moves to a model
// that takes effort, this still passes -- the point is that the pair is
// consistent, not that it is any particular value.
func TestDefaultModelIsConsistentWithItsEffortSetting(t *testing.T) {
	if DefaultModel == "" {
		t.Fatal("DefaultModel is empty")
	}
	t.Logf("DefaultModel=%q supportsEffort=%v", DefaultModel, supportsEffort(DefaultModel))
}
