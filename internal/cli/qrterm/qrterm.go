// Package qrterm draws a QR code in a terminal, two modules per character with half blocks.
package qrterm

import (
	"errors"
	"strings"

	"rsc.io/qr/coding"
)

// quiet is the margin, in modules, that scanners need around a code: ISO/IEC 18004 asks for four. In a terminal the
// white Render paints is all the light there is, since a dark theme's background reads as dark.
const quiet = 4

// Render draws text as a QR code. In colour (a terminal), it paints black on white whatever the terminal's theme;
// without, the blocks are the dark modules, for a light page.
func Render(text string, colour bool) (string, error) {
	code, err := encode(text)
	if err != nil {
		return "", err
	}
	dark := func(x, y int) bool { return code.Black(x-quiet, y-quiet) } // light outside the code
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

// encode makes text's code at level M in the smallest version it fits, as rsc.io/qr does, with the mask of the lowest
// penalty, as ISO/IEC 18004 §7.8.3 asks and ZXing's encoder scores it. rsc.io/qr's own Encode always takes mask 0;
// with the lowest-penalty mask, fewer codes have a patch of data that a scanner takes for a finder pattern in one
// frame, though some still do (#145).
func encode(text string) (*coding.Code, error) {
	enc := encodingOf(text)
	v := coding.Version(coding.MinVersion)
	for ; enc.Bits(v) > v.DataBytes(coding.M)*8; v++ {
		if v == coding.MaxVersion {
			return nil, errors.New("text too long to encode as QR")
		}
	}
	var best *coding.Code
	bestScore := 0
	for mask := range coding.Mask(8) {
		p, err := coding.NewPlan(v, coding.M, mask)
		if err != nil {
			return nil, err
		}
		c, err := p.Encode(enc)
		if err != nil {
			return nil, err
		}
		if score := penalty(c); best == nil || score < bestScore {
			best, bestScore = c, score
		}
	}
	return best, nil
}

// encodingOf picks the smallest of QR's modes that holds text, as rsc.io/qr does.
func encodingOf(text string) coding.Encoding {
	switch {
	case coding.Num(text).Check() == nil:
		return coding.Num(text)
	case coding.Alpha(text).Check() == nil:
		return coding.Alpha(text)
	default:
		return coding.String(text)
	}
}

// penalty scores a code by ISO/IEC 18004's four mask penalty rules, the way ZXing's encoder reads them.
func penalty(c *coding.Code) int {
	n := c.Size
	score := 0

	// N1: five or more modules of one colour in a row or column: 3, and 1 more for each module beyond five.
	// N3: dark-light-dark-dark-dark-light-dark with four light modules on either side (outside the code counts as
	// light): 40 each.
	finder := [7]bool{true, false, true, true, true, false, true}
	for _, line := range [2]func(i, j int) bool{
		func(i, j int) bool { return c.Black(j, i) }, // row i
		func(i, j int) bool { return c.Black(i, j) }, // column i
	} {
		for i := range n {
			run := 0
			for j := range n {
				if j > 0 && line(i, j) == line(i, j-1) {
					run++
				} else {
					run = 1
				}
				if run == 5 {
					score += 3
				} else if run > 5 {
					score++
				}
			}
			for j := 0; j+len(finder) <= n; j++ {
				match := true
				for k, want := range finder {
					if line(i, j+k) != want {
						match = false
						break
					}
				}
				if match && (light(line, i, j-4, j) || light(line, i, j+7, j+11)) {
					score += 40
				}
			}
		}
	}

	// N2: each 2×2 block of one colour: 3.
	darkCount := 0
	for y := range n {
		for x := range n {
			v := c.Black(x, y)
			if v {
				darkCount++
			}
			if x+1 < n && y+1 < n && c.Black(x+1, y) == v && c.Black(x, y+1) == v && c.Black(x+1, y+1) == v {
				score += 3
			}
		}
	}

	// N4: 10 for each full 5% by which the dark modules' share is away from half.
	total := n * n
	score += abs(2*darkCount-total) * 10 / total * 10
	return score
}

// light says whether the modules from from to to (exclusive) on line i are all light; those outside the code are.
func light(line func(i, j int) bool, i, from, to int) bool {
	for j := from; j < to; j++ {
		if line(i, j) {
			return false
		}
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
