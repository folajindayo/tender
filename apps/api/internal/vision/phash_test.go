package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"
)

func testImage(seed int64, quality int) []byte {
	const w, h = 240, 160
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(seed))
	for by := 0; by < h; by += 40 {
		for bx := 0; bx < w; bx += 40 {
			c := color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255}
			for y := by; y < by+40 && y < h; y++ {
				for x := bx; x < bx+40 && x < w; x++ {
					img.Set(x, y, c)
				}
			}
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Bytes()
}

func TestDHashIsStableForTheSameImage(t *testing.T) {
	a, err := DHash(testImage(7, 90))
	if err != nil {
		t.Fatal(err)
	}
	b, err := DHash(testImage(7, 90))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same image gave different hashes: %s vs %s", a, b)
	}
}

// The guard has to survive the browser re-compressing a photo on its way up.
func TestDHashSurvivesRecompression(t *testing.T) {
	a, _ := DHash(testImage(7, 95))
	b, _ := DHash(testImage(7, 60))
	if d := HammingDistance(a, b); d > 6 {
		t.Errorf("re-compression moved the hash too far: distance %d", d)
	}
}

func TestDHashDistinguishesDifferentImages(t *testing.T) {
	a, _ := DHash(testImage(1, 90))
	b, _ := DHash(testImage(2, 90))
	if a == b {
		t.Error("different images must not collide")
	}
}

func TestDHashRejectsNonImages(t *testing.T) {
	if _, err := DHash([]byte("this is not an image")); err == nil {
		t.Error("expected an error for non-image bytes")
	}
}
