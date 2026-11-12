// Package qrtermtest reads back, for tests, a QR code that qrterm drew: it turns the half blocks into an image and
// decodes that with ZXing's reader, the detector many phone scanners use.
package qrtermtest

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Modules returns the code's dark modules, row by row, from the lines of out that draw it (colours stripped).
func Modules(out string) ([][]bool, error) {
	out = ansi.ReplaceAllString(out, "")
	var lines [][]rune
	width := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.ContainsAny(line, "█▀▄") {
			lines = append(lines, []rune(line))
			width = max(width, len(lines[len(lines)-1]))
		}
	}
	if len(lines) == 0 {
		return nil, errors.New("no QR code in the output")
	}
	rows := make([][]bool, 2*len(lines))
	for y, l := range lines {
		top, bottom := make([]bool, width), make([]bool, width)
		for x, c := range l {
			top[x], bottom[x] = c == '█' || c == '▀', c == '█' || c == '▄'
		}
		rows[2*y], rows[2*y+1] = top, bottom
	}
	return rows, nil
}

// Frame decodes the code in out as one camera frame sees it: square on, turned clockwise by the given quarter turns.
func Frame(out string, quarterTurns int) (string, error) {
	rows, err := Modules(out)
	if err != nil {
		return "", err
	}
	for range quarterTurns % 4 {
		rows = turn(rows)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(picture(rows))
	if err != nil {
		return "", err
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", err
	}
	return res.GetText(), nil
}

// Decode reads the code in out as a phone's scanner does, over a few frames: square on first, then turned round,
// then a quarter turn each way. A phone keeps reading frames until one decodes; one frame alone can miss, because
// ZXing's detector stops at the first three finder-like centres it confirms, and a patch of data can pass for the
// bottom-left finder pattern seen from one side and not from another (#145).
func Decode(out string) (string, error) {
	var errs []error
	for _, turns := range []int{0, 2, 1, 3} {
		text, err := Frame(out, turns)
		if err == nil {
			return text, nil
		}
		errs = append(errs, fmt.Errorf("turned %d°: %w", 90*turns, err))
	}
	return "", errors.Join(errs...)
}

// turn rotates rows a quarter turn clockwise.
func turn(rows [][]bool) [][]bool {
	h, w := len(rows), len(rows[0])
	out := make([][]bool, w)
	for x := range w {
		out[x] = make([]bool, h)
		for y := range h {
			out[x][h-1-y] = rows[y][x]
		}
	}
	return out
}

// picture paints rows at six pixels a module, black on white, with eight white modules around them.
func picture(rows [][]bool) *image.Gray {
	const scale, border = 6, 8
	h, w := len(rows), len(rows[0])
	img := image.NewGray(image.Rect(0, 0, (w+2*border)*scale, (h+2*border)*scale))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y, r := range rows {
		for x, dark := range r {
			if !dark {
				continue
			}
			for dy := range scale {
				row := img.Pix[((border+y)*scale+dy)*img.Stride:]
				for dx := range scale {
					row[(border+x)*scale+dx] = 0
				}
			}
		}
	}
	return img
}

// Links returns n setup links whose keys come from a fixed seed, on an IPv4 and an IPv6 home network in turn.
func Links(n int) []string {
	r := rand.New(rand.NewPCG(145, 145))
	out := make([]string, n)
	for i := range out {
		var k [32]byte
		for j := range k {
			k[j] = byte(r.Uint32())
		}
		host := "192.168.1.20"
		if i%2 == 1 {
			host = "[fd00:1::20]"
		}
		out[i] = "http://" + host + ":7020/setup#k=" + base64.RawURLEncoding.EncodeToString(k[:])
	}
	return out
}
