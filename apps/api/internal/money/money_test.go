package money

import "testing"

func TestString(t *testing.T) {
	for _, tc := range []struct {
		in   Kobo
		want string
	}{
		{0, "₦0.00"},
		{5, "₦0.05"},
		{100, "₦1.00"},
		{2000000, "₦20,000.00"},
		{123456789, "₦1,234,567.89"},
		{-2000000, "-₦20,000.00"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Kobo(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFeeFor(t *testing.T) {
	for _, tc := range []struct {
		amount Kobo
		bps    int
		want   Kobo
	}{
		{FromNaira(20000), 50, FromNaira(100)}, // 0.5% of ₦20,000
		{FromNaira(5000), 50, 2500},            // ₦25
		{FromNaira(100), 0, 0},
		{0, 50, 0},
		{-5, 50, 0},
	} {
		if got := FeeFor(tc.amount, tc.bps); got != tc.want {
			t.Errorf("FeeFor(%s, %d) = %d, want %d", tc.amount, tc.bps, got, tc.want)
		}
	}
}

// A fee must never swallow the whole transfer, or the recipient gets nothing.
func TestFeeNeverExceedsAmount(t *testing.T) {
	if got := FeeFor(FromNaira(100), 10000); got >= FromNaira(100) {
		t.Errorf("fee %d must stay below the amount", got)
	}
}

func TestIsDenomination(t *testing.T) {
	if !IsDenomination(FromNaira(500)) {
		t.Error("₦500 is a real banknote")
	}
	if IsDenomination(FromNaira(300)) {
		t.Error("₦300 is not a Nigerian banknote and must be rejected")
	}
}
