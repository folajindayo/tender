// Package money represents naira amounts as integer kobo.
//
// Every monetary value in Tender is an int64 count of kobo (1 naira = 100 kobo).
// Floating point never touches money, at rest or in transit.
package money

import (
	"fmt"
	"strings"
)

// Kobo is an amount in kobo. Signed, because ledger entries are signed.
type Kobo int64

const NairaInKobo Kobo = 100

func FromNaira(n int64) Kobo { return Kobo(n) * NairaInKobo }

func (k Kobo) Naira() int64 { return int64(k) / int64(NairaInKobo) }

// String renders an amount the way a Nigerian user expects to read it: ₦20,000.00
func (k Kobo) String() string {
	neg := k < 0
	if neg {
		k = -k
	}
	naira := int64(k) / 100
	kobo := int64(k) % 100

	digits := fmt.Sprintf("%d", naira)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}

	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, b.String(), kobo)
}

// Denominations of Nigerian banknotes currently in circulation, largest first.
var Denominations = []Kobo{
	FromNaira(1000), FromNaira(500), FromNaira(200),
	FromNaira(100), FromNaira(50), FromNaira(20),
	FromNaira(10), FromNaira(5),
}

// IsDenomination reports whether v is a real Nigerian banknote value. Vision
// output claiming anything else is a recognition error, not a banknote.
func IsDenomination(v Kobo) bool {
	for _, d := range Denominations {
		if d == v {
			return true
		}
	}
	return false
}

// FeeFor computes the platform fee in basis points of amount, with a floor of
// zero. The fee always comes out of the transferred amount, never in addition
// to it -- the sender is handing over physical notes and cannot make change.
func FeeFor(amount Kobo, bps int) Kobo {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	fee := Kobo(int64(amount) * int64(bps) / 10000)
	if fee >= amount {
		fee = amount - 1
	}
	return fee
}
