package qrterm_test

import (
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/cli/qrterm"
	"github.com/ArjunRachithcf/homerslab/internal/cli/qrterm/qrtermtest"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// This key's code, drawn with mask 0 whatever the data, has a patch that ZXing's detector takes for the bottom-left
// finder pattern, square on and tilted by up to ten degrees either way (#145).
const onceFailed = "http://192.168.1.20:7020/setup#k=oHpJllsmCFoXJ0gFEWifvSAMutxV_RXV5U3siML-xNs"

func TestTheCodeOfAKeyThatOnceFailedDecodesSquareOn(t *testing.T) {
	testenv.Isolate(t)
	for _, colour := range []bool{false, true} {
		out, err := qrterm.Render(onceFailed, colour)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := qrtermtest.Frame(out, 0); err != nil || got != onceFailed {
			t.Errorf("colour %v: the code says %q (%v), want %q\n%s", colour, got, err, onceFailed, out)
		}
	}
}

func TestTheCodeDecodesForEveryKey(t *testing.T) {
	testenv.Isolate(t)
	for _, link := range qrtermtest.Links(300) {
		out, err := qrterm.Render(link, false)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := qrtermtest.Decode(out); err != nil || got != link {
			t.Errorf("the code says %q (%v), want %q\n%s", got, err, link, out)
		}
	}
}

// ISO/IEC 18004 asks for four light modules around the code. In a terminal, the white that Render paints is all the
// light there is: a dark theme's background around it reads as dark.
func TestTheCodeHasAQuietZoneOfFourModulesInATerminal(t *testing.T) {
	testenv.Isolate(t)
	out, err := qrterm.Render(onceFailed, true)
	if err != nil {
		t.Fatal(err)
	}
	var painted [][]bool // the painted area, two rows of modules a line
	for line := range strings.SplitSeq(out, "\n") {
		_, rest, ok := strings.Cut(line, "\x1b[30;107m")
		if !ok {
			continue
		}
		rest, _, _ = strings.Cut(rest, "\x1b[0m")
		top, bottom := []bool{}, []bool{}
		for _, c := range rest {
			top, bottom = append(top, c == '█' || c == '▀'), append(bottom, c == '█' || c == '▄')
		}
		painted = append(painted, top, bottom)
	}
	if len(painted) == 0 {
		t.Fatalf("nothing painted in:\n%q", out)
	}
	minX, minY, maxX, maxY := len(painted[0]), len(painted), -1, -1
	for y, row := range painted {
		for x, dark := range row {
			if dark {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	left, top, right, bottom := minX, minY, len(painted[0])-1-maxX, len(painted)-1-maxY
	if left < 4 || top < 4 || right < 4 || bottom < 4 {
		t.Errorf("light modules around the code: left %d, top %d, right %d, bottom %d; want at least 4 each", left, top, right, bottom)
	}
}
