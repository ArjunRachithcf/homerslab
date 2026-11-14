package qrterm

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/makiuchi-d/gozxing/qrcode/encoder"
	"rsc.io/qr"
	"rsc.io/qr/coding"

	"github.com/ArjunRachithcf/homerslab/internal/cli/qrterm/qrtermtest"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// zxingPenalty scores a code by ISO/IEC 18004's four mask penalty rules, as ZXing's encoder does.
func zxingPenalty(bitmap []byte, size, stride int) int {
	m := encoder.NewByteMatrix(size, size)
	for y := range size {
		for x := range size {
			m.SetBool(x, y, bitmap[y*stride+x/8]&(1<<uint(7-x&7)) != 0)
		}
	}
	return encoder.MaskUtil_applyMaskPenaltyRule1(m) + encoder.MaskUtil_applyMaskPenaltyRule2(m) +
		encoder.MaskUtil_applyMaskPenaltyRule3(m) + encoder.MaskUtil_applyMaskPenaltyRule4(m)
}

func TestTheCodeUsesTheMaskWithTheLowestPenalty(t *testing.T) {
	testenv.Isolate(t)
	for _, text := range append(qrtermtest.Links(100), "HTTP://192.168.1.20:7020/", "20261112") {
		got, err := encode(text)
		if err != nil {
			t.Fatal(err)
		}
		// Mask 0 is what rsc.io/qr draws: the same mode and version as the library's.
		lib, err := qr.Encode(text, qr.M)
		if err != nil {
			t.Fatal(err)
		}
		if p, err := coding.NewPlan(coding.Version(versionOf(got.Size)), coding.M, 0); err != nil {
			t.Fatal(err)
		} else if c, err := p.Encode(encodingOf(text)); err != nil || !bytes.Equal(c.Bitmap, lib.Bitmap) {
			t.Errorf("%q: with mask 0, the code isn't rsc.io/qr's (%v)", text, err)
		}
		var want *coding.Code
		best := -1
		for mask := range coding.Mask(8) {
			p, err := coding.NewPlan(coding.Version(versionOf(got.Size)), coding.M, mask)
			if err != nil {
				t.Fatal(err)
			}
			c, err := p.Encode(encodingOf(text))
			if err != nil {
				t.Fatal(err)
			}
			score := zxingPenalty(c.Bitmap, c.Size, c.Stride)
			if best < 0 || score < best {
				want, best = c, score
			}
			if got := penalty(c); got != score {
				t.Errorf("%q, mask %d: penalty %d, ZXing's %d", text, mask, got, score)
			}
		}
		if !bytes.Equal(got.Bitmap, want.Bitmap) {
			t.Errorf("%q: the code isn't the one with the lowest penalty (%d)", text, best)
		}
	}
}

func versionOf(size int) int { return (size - 17) / 4 }

// Any grid of modules, sparse or dense, scores as ZXing scores it.
func TestThePenaltyIsZXings(t *testing.T) {
	testenv.Isolate(t)
	r := rand.New(rand.NewPCG(145, 18004))
	for i := range 400 {
		size := 21 + 4*(i%10)
		dense := float64(i%9+1) / 10
		c := &coding.Code{Size: size, Stride: (size + 7) / 8}
		c.Bitmap = make([]byte, c.Stride*size)
		for y := range size {
			for x := range size {
				if r.Float64() < dense {
					c.Bitmap[y*c.Stride+x/8] |= 1 << uint(7-x&7)
				}
			}
		}
		if got, want := penalty(c), zxingPenalty(c.Bitmap, c.Size, c.Stride); got != want {
			t.Fatalf("a %d×%d grid, %.0f%% dark: penalty %d, ZXing's %d", size, size, 100*dense, got, want)
		}
	}
}
