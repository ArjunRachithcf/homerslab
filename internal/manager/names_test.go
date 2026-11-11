package manager_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Letters, numbers, symbols and punctuation are written as they are, and every mark and invisible character as an
// escape, so the source shows each one.

// Real names, in the scripts a household writes them in, and the emoji people put in a household's name: none of
// them is refused (the independent review's no-false-positive set).
var realNames = []struct{ what, name string }{
	{"German", "Jürgen Groß"},
	{"German", "Zoë Müller-Lüdenscheidt"},
	{"French", "Hélène Lefèvre-Côté"},
	{"French", "François Œuvray"},
	{"Vietnamese", "Nguyễn Thị Minh Khai"},
	{"Polish", "Łukasz Żółkiewski"},
	{"Turkish", "Işıl Çağlar Öztürk"},
	{"Russian", "Ёлка Иванова"},
	{"Greek", "Αλέξανδρος Παπαδόπουλος"},
	{"Hebrew with niqqud", "ש\u05b8\u05c1לו\u05b9ם כ\u05b9\u05bcה\u05b5ן"},
	{"Arabic", "محمد عبد الله"},
	{"Persian with a ZWNJ", "زهرا مرادی\u200cنژاد"},
	{"Urdu", "عمران خان"},
	{"Hindi", "प\u094dर\u093fय\u0902क\u093e च\u094bपड\u093c\u093e"},
	{"Tamil", "ம\u0bc1த\u0bcdத\u0bc1லட\u0bcdச\u0bc1ம\u0bbf"},
	{"Telugu", "వ\u0c46\u0c02కట\u0c47శ\u0c4dవర ర\u0c3eవ\u0c41"},
	{"Kannada", "ಕ\u0cc1ವ\u0cc6\u0c82ಪ\u0cc1"},
	{"Malayalam with a chillu ZWJ", "അവന\u0d4d\u200d മ\u0d4bഹന\u0d4d\u200dല\u0d3eല\u0d4d\u200d"},
	{"Bengali", "রব\u09c0ন\u09cdদ\u09cdরন\u09beথ ঠ\u09beক\u09c1র"},
	{"Sinhala with a ZWJ", "ශ\u0dca\u200dර\u0dd3 ල\u0d82ක\u0dcf"},
	{"Thai", "สมชาย ใจด\u0e35"},
	{"Khmer", "ស\u17bbខ ច\u17b6ន\u17cb"},
	{"Burmese", "အ\u1031\u102cင\u103aဆန\u103a\u1038စ\u102fက\u103cည\u103a"},
	{"Chinese", "王小明"},
	{"Japanese", "山田 花子"},
	{"Japanese with an ideographic variant", "葛\U000e0100飾"},
	{"Korean", "김민준"},
	{"Korean with decomposed jamo", "김민준"},
	{"Amharic", "ኃይሌ ገብረሥላሴ"},
	{"Armenian", "Արամ Խաչատրյան"},
	{"Georgian", "ნინო ბურჯანაძე"},
	{"Mongolian, Cyrillic", "Батбаяр"},
	{"Mongolian, traditional", "ᠮᠣᠩᠭᠣᠯ"},
	{"Mongolian with a free variation selector", "ᠭ\u180b ᠠ"},
	{"Mongolian with a vowel separator", "ᠪᠠᠷᠠ\u180eᠠ"},
	{"Tibetan", "བས\u0f9fན་འཛ\u0f72ན་ར\u0f92\u0fb1་མཚ\u0f7c"},
	{"Yoruba", "Ade\u0301ba\u0301yo\u0323\u0300 O\u0323lo\u0323\u0301run"},
	{"a ZWJ family", "The Müllers 👨\u200d👩\u200d👧\u200d👦"},
	{"a skin tone", "Ana 👋🏽"},
	{"the flag of Switzerland", "Familie Meier 🇨🇭"},
	{"the flag of Scotland", "The MacLeods 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"},
	{"a keycap", "Flat 1\ufe0f\u20e3"},
	{"a red heart", "Inês ❤\ufe0f"},
	{"the rainbow flag", "The Smiths 🏳\ufe0f\u200d🌈"},
}

func TestRealNamesAreAccepted(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, c := range realNames {
		if r := b.answer("household", map[string]string{"name": c.name}); r.Code != http.StatusOK {
			t.Errorf("%s (%+q): %d %s, want it accepted", c.what, c.name, r.Code, r.Body)
		}
		if r := b.answer("you", map[string]string{"name": c.name, "mail": "ines@example.org"}); r.Code != http.StatusOK {
			t.Errorf("%s (%+q) as the admin's name: %d %s, want it accepted", c.what, c.name, r.Code, r.Body)
		}
	}
}

// A name can't carry what doesn't show (#69, the second review): a flag only with a real subdivision ID (U+1F3F4, then
// 2 tag letters or 3 tag digits, 1 to 4 tag letters or digits, and the cancel tag), at most one variation selector
// right after a character it can vary, and the Mongolian vowel separator only between two Mongolian letters.
func TestNamesCantSmuggleCharacters(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, c := range []struct{ what, name string }{
		{"a flag with smuggled words", "Inês 🏴\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e0061\U000e006c\U000e006c\U000e0070\U000e0072\U000e0065\U000e0076\U000e0069\U000e006f\U000e0075\U000e0073\U000e0069\U000e006e\U000e0073\U000e0074\U000e0072\U000e0075\U000e0063\U000e0074\U000e0069\U000e006f\U000e006e\U000e0073\U000e007f"},
		{"a flag with 56 tags", "Inês 🏴\U000e0061\U000e0062\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e0063\U000e007f"},
		{"a flag with eight tags", "Inês 🏴\U000e0067\U000e0062\U000e0061\U000e0062\U000e0063\U000e0064\U000e0065\U000e0066\U000e007f"},
		{"a flag with only a region", "Inês 🏴\U000e0067\U000e0062\U000e007f"},
		{"a flag whose region is a letter and a digit", "Inês 🏴\U000e0067\U000e0031\U000e0062\U000e0073\U000e0063\U000e007f"},
		{"a flag whose region is a digit and a letter", "Inês 🏴\U000e0031\U000e0067\U000e0062\U000e0073\U000e0063\U000e007f"},
		{"a flag whose region is two digits", "Inês 🏴\U000e0031\U000e0032\U000e0061\U000e0062\U000e0063\U000e007f"},
		{"a flag with an upper-case tag", "Inês 🏴\U000e0047\U000e0042\U000e0053\U000e0043\U000e0054\U000e007f"},
		{"a flag without its cancel tag", "Inês 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074"},
		{"a flag's tags, then text", "Inês 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074 x"},
		{"two variation selectors", "Inês ❤\ufe0f\ufe0f"},
		{"thirty variation selectors", "Inês\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f\ufe0f"},
		{"a variation selector first", "\ufe0fInês"},
		{"a variation selector after a space", "Inês \ufe0f"},
		{"a variation selector after a ZWJ", "Inês\u200d\ufe0f"},
		{"an ideographic variant after another", "葛\U000e0100\U000e0101飾"},
		{"a vowel separator between Latin letters", "a\u180eb"},
		{"a vowel separator at the start", "\u180eᠠᠠ"},
		{"a vowel separator at the end", "ᠠᠠ\u180e"},
	} {
		r := b.answer("household", map[string]string{"name": c.name})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"hidden-characters"`) {
			t.Errorf("%s (%+q): %d %s, want 400 hidden-characters", c.what, c.name, r.Code, r.Body)
		}
	}
}

// A name that shows as blank is no name: it needs a letter, number or symbol, not counting the braille blank.
func TestANameThatShowsAsBlankIsMissing(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, c := range []struct{ what, name string }{
		{"a ZWJ alone", "\u200d"},
		{"a ZWNJ alone", "\u200c"},
		{"a ZWJ and a ZWNJ", "\u200d\u200c"},
		{"a braille blank", "⠀"},
		{"three braille blanks", "⠀⠀⠀"},
		{"a combining grapheme joiner", "\u034f"},
		{"Khmer inherent vowels", "\u17b4\u17b5"},
		{"a lone accent", "\u0301"},
		{"two lone accents", "\u0301\u0302"},
	} {
		r := b.answer("household", map[string]string{"name": c.name})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"missing"`) {
			t.Errorf("%s (%+q): %d %s, want 400 missing", c.what, c.name, r.Code, r.Body)
		}
	}
}
