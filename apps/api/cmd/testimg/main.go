// Command testimg writes a deterministic JPEG, used to exercise the pledge
// path without a camera. A different seed produces a visually different image,
// and therefore a different perceptual hash.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
)

func main() {
	seed := flag.Int64("seed", 1, "changes the generated pattern")
	out := flag.String("out", "note.jpg", "output path")
	flag.Parse()

	const w, h = 480, 320
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(*seed))

	// Blocky, high-contrast bands so the difference hash has real structure.
	const cell = 40
	for by := 0; by < h; by += cell {
		for bx := 0; bx < w; bx += cell {
			c := color.RGBA{
				R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)), A: 255,
			}
			for y := by; y < by+cell && y < h; y++ {
				for x := bx; x < bx+cell && x < w; x++ {
					img.Set(x, y, c)
				}
			}
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
