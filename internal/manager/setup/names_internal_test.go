package setup

import (
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Every recommended (RGI) emoji sequence of Unicode 17.0 is a name, or part of one: alone, and after a word (#99, the
// fifth review). The exceptions (nfkcRewritten) are the emoji NFKC turns into letters or punctuation, after which
// their VS16 has nothing to follow.
func TestEveryRecommendedEmojiSequenceIsAccepted(t *testing.T) {
	testenv.Isolate(t)
	refused := 0
	for _, c := range rgiSequences {
		if nfkcRewritten[c.seq] {
			continue
		}
		for _, n := range []string{c.seq, "Ana " + c.seq} {
			if _, why := name(n); why != "" {
				refused++
				t.Errorf("%s %q (%+q): %s, want it accepted", c.kind, c.name, n, why)
			}
		}
	}
	if refused == 0 && len(rgiSequences) < 3900 {
		t.Errorf("only %d sequences: the generated list is short", len(rgiSequences))
	}
}

// nfkcRewritten are the RGI sequences whose base NFKC rewrites into letters, punctuation or a kana or ideograph (U+203C
// to !!, U+2049 to !?, U+2122 to TM, U+2139 to i, U+24C2 to M, U+3297 to 祝, U+3299 to 秘, U+1F202 to サ, U+1F237 to
// 月), so the VS16 after it follows no emoji. The name keeps the rewritten form without the VS16 if it is typed so.
var nfkcRewritten = map[string]bool{
	"\u203c\ufe0f":     true,
	"\u2049\ufe0f":     true,
	"\u2122\ufe0f":     true,
	"\u2139\ufe0f":     true,
	"\u24c2\ufe0f":     true,
	"\u3297\ufe0f":     true,
	"\u3299\ufe0f":     true,
	"\U0001f202\ufe0f": true,
	"\U0001f237\ufe0f": true,
}
