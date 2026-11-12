package setup

//go:generate go run emoji_gen.go

import (
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/secure/precis"
	"golang.org/x/text/unicode/norm"
)

// Names (yours, the household's) reach terminals, mail headers and other people's screens, so they carry nothing that
// doesn't show (#69). A name is what PRECIS's Nickname profile (RFC 8266) makes of it: non-ASCII spaces mapped to
// spaces, NFKC, spaces trimmed and collapsed; and it must pass the profile, which refuses controls, format and other
// default-ignorable characters, unassigned code points, private use and the rest of what FreeformClass disallows.
//
// The profile refuses some of what people really type. Exactly these pass, through exceptions, and nothing wider:
//   - an emoji ZWJ sequence: a ZWJ between two emoji, by Unicode's emoji properties (emojiEndsAt, emojiStartsAt);
//   - VS15 or VS16 (U+FE0E, U+FE0F) right after a base of an emoji variation sequence (emoji-variation-sequences.txt),
//     or after a keycap's 0–9, # or * before U+20E3;
//   - the three flags made of tags that Unicode recommends: England's, Scotland's and Wales's;
//   - an ideographic variation selector (U+E0100–U+E01EF) right after a unified ideograph (Unified_Ideograph: not
//     the iteration mark, the ideographic zero or a radical);
//   - a Mongolian free variation selector (U+180B–U+180D, U+180F) right after a Mongolian letter, and the Mongolian
//     vowel separator (U+180E) between two;
//   - the Arabic tatweel (U+0640) between two Arabic letters.
//
// And a name must show: it needs a letter, number or symbol, or a keycap (#, * or a digit, VS16, U+20E3). A few
// characters draw nothing or stand for broken bytes, though the profile allows them, so they are refused
// (drawsNothing). At most MarksMax combining marks may follow one another (#124).
func name(typed string) (string, string) {
	n := nickname(typed)
	switch {
	case n == "":
		return "", Missing
	case strings.ContainsFunc(n, drawsNothing):
		return "", HiddenCharacters
	case tooManyMarks(n):
		return "", TooManyMarks
	}
	plain := withoutExceptions(n)
	if out, err := precis.Nickname.String(plain); err != nil || out != plain {
		return "", HiddenCharacters
	}
	switch {
	case !strings.ContainsFunc(n, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r) }) &&
		!hasKeycap(n) && !hasEmojiPresentation(n):
		return "", Missing // marks, joiners and punctuation alone show as blank, or nearly
	case utf8.RuneCountInString(n) > NameMax:
		return "", TooLong
	}
	return n, ""
}

// MarksMax is the most combining marks a name may stack on one letter (#124): Zalgo text draws over the lines around it.
const MarksMax = 4

// tooManyMarks says whether n has more than MarksMax combining marks (Mn, Mc, Me) in a row. Every real name's stacks are
// shorter: Arabic vowel signs, Hebrew points, Thai and Hindi, a Tibetan stack. The combining grapheme joiner (U+034F)
// isn't counted and doesn't break a run: NFKC inserts it after 30 marks, and a name made of it is refused as hidden.
func tooManyMarks(n string) bool {
	run := 0
	for _, r := range n {
		switch {
		case r == '\u034f':
		case unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me):
			if run++; run > MarksMax {
				return true
			}
		default:
			run = 0
		}
	}
	return false
}

// nickname is the Nickname profile's preparation, which leaves the exceptions as they are: non-ASCII spaces to
// spaces, NFKC, then spaces trimmed and runs of them collapsed. Only spaces: tabs, line breaks and the rest stay, for
// the profile to refuse.
func nickname(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Zs, r) {
			return ' '
		}
		return r
	}, s)
	var words []string
	for _, w := range strings.Split(norm.NFKC.String(s), " ") {
		if w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// withoutExceptions is n without the characters the exceptions allow where they are, for the profile to judge the rest.
// Anything an exception doesn't cover stays, and the profile refuses it.
func withoutExceptions(n string) string {
	rs := []rune(n)
	drop := make([]bool, len(rs))
	at := func(i int) rune {
		if i < 0 || i >= len(rs) {
			return 0
		}
		return rs[i]
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\U0001f3f4':
			if k := rgiFlagTags(rs[i+1:]); k > 0 {
				for j := i + 1; j <= i+k; j++ {
					drop[j] = true
				}
				i += k
			}
		case r == '\u200d':
			drop[i] = emojiEndsAt(rs, i-1) && emojiStartsAt(rs, i+1)
		case r == '\ufe0e' || r == '\ufe0f':
			p := at(i - 1)
			drop[i] = !keycapBase(p) && unicode.Is(variationBase, p) || keycapBase(p) && at(i+1) == '\u20e3'
		case r >= '\U000e0100' && r <= '\U000e01ef':
			drop[i] = unicode.Is(unicode.Unified_Ideograph, at(i-1))
		case r >= '\u180b' && r <= '\u180d' || r == '\u180f':
			drop[i] = mongolianLetter(at(i - 1))
		case r == '\u180e':
			drop[i] = mongolianLetter(at(i-1)) && mongolianLetter(at(i+1))
		case r == '\u0640':
			drop[i] = arabicLetter(at(i-1)) && arabicLetter(at(i+1))
		}
	}
	var b strings.Builder
	for i, r := range rs {
		if !drop[i] {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rgiFlags are the subdivision flags Unicode recommends: England, Scotland, Wales.
var rgiFlags = []string{"gbeng", "gbsct", "gbwls"}

// rgiFlagTags is how many runes at the start of rs are one of rgiFlags's tags, the cancel tag included; or none.
func rgiFlagTags(rs []rune) int {
	for _, id := range rgiFlags {
		if len(rs) < len(id)+1 || rs[len(id)] != '\U000e007f' {
			continue
		}
		match := true
		for j, c := range id {
			match = match && rs[j] == 0xe0000+c
		}
		if match {
			return len(id) + 1
		}
	}
	return 0
}

// An emoji, for an emoji ZWJ sequence (UTS #51), goes by Unicode's emoji properties (emoji_tables.go): a character
// drawn as an emoji by default (Emoji_Presentation), or an Emoji character with VS16, either perhaps with a skin tone
// (Emoji_Modifier). Not a keycap's ASCII base, and not a skin tone on its own.

// emojiEndsAt says whether an emoji ends at rs[j].
func emojiEndsAt(rs []rune, j int) bool {
	if j >= 0 && unicode.Is(emojiModifier, rs[j]) {
		j-- // a skin tone follows an Emoji_Modifier_Base, which may be drawn as text alone (U+26F9, U+1F3CB, U+1F3CC, U+1F575)
		return j >= 0 && unicode.Is(emojiModifierBase, rs[j])
	}
	if j >= 1 && rs[j] == '\ufe0f' {
		return unicode.Is(variationBase, rs[j-1]) && !keycapBase(rs[j-1])
	}
	return j >= 0 && emojiAlone(rs[j])
}

// emojiStartsAt says whether an emoji starts at rs[i].
func emojiStartsAt(rs []rune, i int) bool {
	if i >= len(rs) || keycapBase(rs[i]) || unicode.Is(emojiModifier, rs[i]) {
		return false
	}
	return unicode.Is(emojiPresentation, rs[i]) ||
		i+1 < len(rs) && rs[i+1] == '\ufe0f' && unicode.Is(emojiProperty, rs[i]) && unicode.Is(variationBase, rs[i])
}

// emojiAlone is a character drawn as an emoji without a VS16, and not a skin tone.
func emojiAlone(r rune) bool {
	return unicode.Is(emojiPresentation, r) && !unicode.Is(emojiModifier, r)
}

// hasEmojiPresentation says whether s holds an emoji drawn as one by its VS16, such as the wavy dash (U+3030) or the
// part alternation mark (U+303D), which are punctuation without it.
func hasEmojiPresentation(s string) bool {
	rs := []rune(s)
	for i := 1; i < len(rs); i++ {
		if rs[i] == '\ufe0f' && unicode.Is(variationBase, rs[i-1]) && !keycapBase(rs[i-1]) {
			return true
		}
	}
	return false
}

// hasKeycap says whether s holds a keycap: #, * or a digit, VS16 perhaps, and U+20E3.
func hasKeycap(s string) bool {
	rs := []rune(s)
	for i, r := range rs {
		if r == '\u20e3' && i > 0 && (keycapBase(rs[i-1]) || i > 1 && rs[i-1] == '\ufe0f' && keycapBase(rs[i-2])) {
			return true
		}
	}
	return false
}

func keycapBase(r rune) bool { return r >= '0' && r <= '9' || r == '#' || r == '*' }

func mongolianLetter(r rune) bool { return unicode.Is(unicode.Mongolian, r) && unicode.IsLetter(r) }

func arabicLetter(r rune) bool { return unicode.Is(unicode.Arabic, r) && unicode.IsLetter(r) }

// mailAddress is a mail address as it is kept: a local part in ASCII as typed, or else in PRECIS's UsernameCaseMapped
// profile; a domain as IDNA's lookup profile has it, in Unicode; nothing invisible, no ZWJ or ZWNJ, whether typed or
// decoded from an A-label; and RFC 5321's lengths at most.
func mailAddress(typed string) (string, string) {
	s := strings.TrimSpace(norm.NFC.String(typed))
	switch {
	case s == "":
		return "", Missing
	case strings.ContainsFunc(s, invisible):
		return "", HiddenCharacters
	case len(s) > MailMax:
		return "", TooLong
	}
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "", NotAMail
	}
	local, domain := s[:at], s[at+1:]
	if !ascii(local) {
		var err error
		if local, err = precis.UsernameCaseMapped.String(local); err != nil {
			return "", NotAMail
		}
	}
	a, err := lookup.ToASCII(domain)
	if err != nil {
		return "", NotAMail
	}
	if domain, err = lookup.ToUnicode(a); err != nil || !strings.Contains(domain, ".") {
		return "", NotAMail
	}
	addr := local + "@" + domain
	if strings.ContainsFunc(addr, invisible) { // what an A-label (xn--) decodes to is checked too
		return "", HiddenCharacters
	}
	if len(local) > LocalMax {
		return "", TooLong
	}
	if p, err := mail.ParseAddress(addr); err != nil || p.Address != addr || p.Name != "" {
		return "", NotAMail
	}
	if len(addr) > MailMax || len(local)+1+len(a) > MailMax {
		return "", TooLong
	}
	return addr, ""
}

// lookup is IDNA's lookup profile (idna.Lookup), which also checks DNS's lengths: a label of 63 octets at most.
var lookup = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false), idna.VerifyDNSLength(true))

// LocalMax is the longest local part of a mail address, in octets (RFC 5321).
const LocalMax = 64

// invisible is a character that doesn't show or changes how the text around it reads: a control, a format character,
// a line or paragraph separator, or one Unicode says to ignore by default. And the three that draw nothing that names
// refuse too.
func invisible(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point,
		unicode.Variation_Selector) || drawsNothing(r)
}

// drawsNothing is a character PRECIS allows that draws nothing, or stands for bytes that weren't text: the braille
// blank (U+2800), the musical null notehead (U+1D159), the Khitan filler (U+16FE4) and the replacement character
// (U+FFFD).
func drawsNothing(r rune) bool {
	return r == '\u2800' || r == '\U0001d159' || r == '\U00016fe4' || r == '\ufffd'
}

func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
