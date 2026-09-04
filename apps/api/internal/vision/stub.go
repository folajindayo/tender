package vision

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"

	"tender/api/internal/domain"
	"tender/api/internal/money"
)

// Stub is a deterministic recognizer used by the test suite so that tests do
// not depend on a network call or on model output varying between runs.
//
// It is not a product code path. Selecting it requires VISION_MODE=stub, which
// production configuration never sets.
type Stub struct{}

func (Stub) Mode() string { return "stub" }

func (Stub) Analyze(_ context.Context, raw []byte, declared money.Kobo) (*Result, error) {
	dhash, err := DHash(raw)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Mode:        "stub",
		ImageSHA256: SHA256(raw),
		ImageDHash:  dhash,
		Confidence:  0.93,
	}
	res.Notes = breakdown(declared, dhash)
	for _, n := range res.Notes {
		res.Total += n.Denomination
	}
	return res, nil
}

// breakdown expresses an amount as real banknotes, largest denomination first,
// giving each note an identity derived from the photograph so that the same
// image always yields the same notes.
func breakdown(total money.Kobo, dhash string) []domain.Note {
	var notes []domain.Note
	remaining := total
	idx := 0

	for _, d := range money.Denominations {
		for remaining >= d {
			remaining -= d
			notes = append(notes, domain.Note{
				Denomination:     d,
				Serial:           stubSerial(dhash, idx),
				SerialConfidence: 0.9,
				PHash:            fmt.Sprintf("%s:%02d", dhash, idx),
			})
			idx++
			if idx > 400 {
				return notes
			}
		}
	}
	return notes
}

func stubSerial(dhash string, idx int) string {
	var seed uint64
	fmt.Sscanf(dhash, "%016x", &seed)

	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, seed+uint64(idx)*2654435761)
	r := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(b))))

	letters := []byte("ABCDEFGHJKLMNPRSTUVWXYZ")
	return fmt.Sprintf("%c%c/%02d %07d",
		letters[r.Intn(len(letters))], letters[r.Intn(len(letters))],
		r.Intn(100), r.Intn(10000000))
}
