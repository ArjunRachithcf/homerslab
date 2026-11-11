// Package qrterm draws a QR code in a terminal, two modules per character with half blocks.
package qrterm

import (
	"strings"

	"rsc.io/qr"
)

// quiet is the margin, in modules, that scanners need around a code.
const quiet = 2

// Render draws text as a QR code. In colour (a terminal), it paints black on white whatever the terminal's theme;
// without, the blocks are the dark modules, for a light page.
func Render(text string, colour bool) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	n := code.Size + 2*quiet
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.WriteString("  ")
		if colour {
			b.WriteString("\x1b[30;107m")
		}
		for x := range n {
			switch top, bottom := dark(x, y), dark(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		if colour {
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
