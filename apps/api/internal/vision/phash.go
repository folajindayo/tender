package vision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
)

// DHash computes a 64-bit difference hash of an image.
//
// The image is reduced to 9x8 greyscale and each pixel compared with its right
// neighbour, producing one bit per comparison. The result survives re-encoding,
// rescaling and mild compression, so re-uploading the "same" photo -- even
// after the browser has re-compressed it -- still collides.
func DHash(raw []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}

	const w, h = 9, 8
	grid := downsampleGrey(img, w, h)

	var bits uint64
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			if grid[y*w+x] > grid[y*w+x+1] {
				bits |= 1 << uint(i)
			}
			i++
		}
	}
	return fmt.Sprintf("%016x", bits), nil
}

// downsampleGrey box-samples img into a w*h greyscale grid.
func downsampleGrey(img image.Image, w, h int) []float64 {
	b := img.Bounds()
	out := make([]float64, w*h)

	cellW := float64(b.Dx()) / float64(w)
	cellH := float64(b.Dy()) / float64(h)

	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			x0 := b.Min.X + int(float64(cx)*cellW)
			x1 := b.Min.X + int(float64(cx+1)*cellW)
			y0 := b.Min.Y + int(float64(cy)*cellH)
			y1 := b.Min.Y + int(float64(cy+1)*cellH)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}

			var sum float64
			var n int
			for y := y0; y < y1 && y < b.Max.Y; y++ {
				for x := x0; x < x1 && x < b.Max.X; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					// Rec. 601 luma, on 16-bit channel values.
					sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
					n++
				}
			}
			if n > 0 {
				out[cy*w+cx] = sum / float64(n)
			}
		}
	}
	return out
}

// SHA256 is the exact-bytes identifier for a photograph.
func SHA256(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// HammingDistance counts differing bits between two dhash strings. Kept for
// near-duplicate scoring; the unique index uses exact equality.
func HammingDistance(a, b string) int {
	if len(a) != len(b) {
		return 64
	}
	var x, y uint64
	fmt.Sscanf(a, "%016x", &x)
	fmt.Sscanf(b, "%016x", &y)
	d := 0
	for v := x ^ y; v != 0; v &= v - 1 {
		d++
	}
	return d
}
