package manager_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Names and mail addresses (#69). A name goes through PRECIS's Nickname profile (RFC 8266), with a short exception set
// for what people really type that the profile refuses: emoji sequences, three flags, ideographic and Mongolian
// variants, the Mongolian vowel separator and the Arabic tatweel. The sets here include the third independent
// review's: its attacks and its real names.
//
// Letters, numbers, symbols and punctuation are written as they are, and every mark and invisible character as an
// escape, so the source shows each one.

// Real names, in the scripts a household writes them in, and the emoji people put in a household's name: none of
// them is refused.
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
	{"the flag of Wales", "The Joneses 🏴\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f"},
	{"the flag of England", "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f Smith"},
	{"a keycap", "Flat 1\ufe0f\u20e3"},
	{"a red heart", "Inês ❤\ufe0f"},
	{"the rainbow flag", "The Smiths 🏳\ufe0f\u200d🌈"},
	{"the pirate flag", "Pirates 🏴\u200d☠\ufe0f"},
	{"a kissing couple with skin tones", "👩🏽\u200d❤\ufe0f\u200d💋\u200d👨🏻"},
	{"an Arabic tatweel", "محـمد"},
	{"Hebrew letters and digits", "שלום 123"},
	{"a woman bouncing a ball, with a skin tone", "⛹🏽\u200d♀\ufe0f"},
	{"a man lifting weights, with a skin tone", "🏋🏽\u200d♂\ufe0f"},
	{"a woman golfing, with a skin tone", "🏌🏽\u200d♀\ufe0f"},
	{"a man detective, with a skin tone", "🕵🏽\u200d♂\ufe0f"},
	{"a wavy dash", "〰\ufe0f"},
	{"a keycap number sign alone", "#\ufe0f\u20e3"},
	{"a keycap asterisk alone", "*\ufe0f\u20e3"},
	{"two smileys joined", "😀\u200d😀"},
	{"the third review's set", "Søren Kierkegaard"},
	{"the third review's set", "Þórður Ægisson"},
	{"the third review's set", "José María Aznar-López"},
	{"the third review's set", "François-Xavier O'Brien"},
	{"the third review's set", "O’Neill"},
	{"the third review's set", "İsmail Özdemir"},
	{"the third review's set", "Đặng Văn Lâm"},
	{"the third review's set", "Nguye\u0302\u0303n"},
	{"the third review's set", "Ọlọ\u0301run Adébáyọ\u0300"},
	{"the third review's set", "Kamehameha ʻIolani"},
	{"the third review's set", "Māui Tūhoe"},
	{"the third review's set", "Diné ą\u0301"},
	{"the third review's set", "Dvořák Šťastný"},
	{"the third review's set", "Bjørn Ødegård"},
	{"the third review's set", "Ærøskøbing"},
	{"the third review's set", "Mäkelä-Hämäläinen"},
	{"the third review's set", "Ağaoğlu"},
	{"the third review's set", "ȘtefanȚepeș"},
	{"the third review's set", "Дмитрий Шостакович"},
	{"the third review's set", "Ґанна Їжак"},
	{"the third review's set", "Ђорђе Џаковић"},
	{"the third review's set", "Αλέξανδρος"},
	{"the third review's set", "Արամ Խաչատրյան"},
	{"the third review's set", "ნინო ბურჯანაძე"},
	{"the third review's set", "דוד בן־גוריון"},
	{"the third review's set", "ש\u05b8\u05c1לו\u05b9ם"},
	{"the third review's set", "י\u05b4צ\u05b0ח\u05b8ק ר\u05b7ב\u05b4\u05bcין"},
	{"the third review's set", "م\u064fح\u064eم\u064e\u0651د"},
	{"the third review's set", "محـمد"},
	{"the third review's set", "فاطمه\u200cزهرا"},
	{"the third review's set", "می\u200cخواهم"},
	{"the third review's set", "کوردی\u200cیەکان"},
	{"the third review's set", "عمران خان"},
	{"the third review's set", "ئۇيغۇر"},
	{"the third review's set", "މ\u07aaހ\u07a6އ\u07b0މ\u07a6ދ\u07aa"},
	{"the third review's set", "ܐܪܡܝܐ"},
	{"the third review's set", "ߒߞߏ"},
	{"the third review's set", "अर\u094dज\u0941न र\u093eच\u093fत"},
	{"the third review's set", "क\u094d\u200dष"},
	{"the third review's set", "क\u094d\u200cष"},
	{"the third review's set", "রব\u09c0ন\u09cdদ\u09cdরন\u09beথ ঠ\u09beক\u09c1র"},
	{"the third review's set", "ச\u0bc1ப\u0bcdப\u0bbfரமண\u0bbfயம\u0bcd"},
	{"the third review's set", "వ\u0c46\u0c02కట\u0c47శ\u0c4dవర"},
	{"the third review's set", "ಕ\u0cc1ವ\u0cc6\u0c82ಪ\u0cc1"},
	{"the third review's set", "മലയ\u0d3eള\u0d02"},
	{"the third review's set", "ന\u0d4d\u200d"},
	{"the third review's set", "ശ\u0d4dര\u0d40"},
	{"the third review's set", "ශ\u0dca\u200dර\u0dd3 ල\u0d82ක\u0dcf"},
	{"the third review's set", "ગ\u0abe\u0a82ધ\u0ac0"},
	{"the third review's set", "ਗ\u0a41ਰ\u0a42 ਨ\u0a3eਨਕ"},
	{"the third review's set", "ଓଡ\u0b3c\u0b3fଆ"},
	{"the third review's set", "অসম\u09c0য\u09bc\u09be"},
	{"the third review's set", "न\u0947प\u093eल\u0940"},
	{"the third review's set", "मर\u093eठ\u0940"},
	{"the third review's set", "สมชาย ใจด\u0e35"},
	{"the third review's set", "ສ\u0ebbມພອນ"},
	{"the third review's set", "ស\u17bbខ ហ\u17c1ង"},
	{"the third review's set", "ស\u17d2រ\u17b8"},
	{"the third review's set", "အ\u1031\u102cင\u103aဆန\u103a\u1038စ\u102fက\u103cည\u103a"},
	{"the third review's set", "བས\u0f9fན་འཛ\u0f72ན་ར\u0f92\u0fb1་མཚ\u0f7c"},
	{"the third review's set", "ꦗꦮ"},
	{"the third review's set", "王小明"},
	{"the third review's set", "山田 太郎"},
	{"the third review's set", "やまだ たろう"},
	{"the third review's set", "ヤマダ・タロウ"},
	{"the third review's set", "葛\U000e0100城"},
	{"the third review's set", "邊\U000e0101"},
	{"the third review's set", "김민준"},
	{"the third review's set", "각"},
	{"the third review's set", "ᠮᠣᠩᠭᠣᠯ"},
	{"the third review's set", "ᠨᠠ\u180eᠠ"},
	{"the third review's set", "ᠭ\u180b"},
	{"the third review's set", "ᠭ\u180f"},
	{"the third review's set", "ኃይለ ሥላሴ"},
	{"the third review's set", "ᏣᎳᎩ"},
	{"the third review's set", "ᐃᓄᒃᑎᑐᑦ"},
	{"the third review's set", "ⵜⴰⵎⴰⵣⵉⵖⵜ"},
	{"the third review's set", "ᓀᐦᐃᔭᐍᐏᐣ"},
	{"the third review's set", "The Müllers 🏡"},
	{"the third review's set", "Family 👨\u200d👩\u200d👧\u200d👦"},
	{"the third review's set", "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f Clan MacLeod"},
	{"the third review's set", "🏴\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f Jones"},
	{"the third review's set", "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f"},
	{"the third review's set", "Pirates 🏴\u200d☠\ufe0f"},
	{"the third review's set", "❤\ufe0f Home"},
	{"the third review's set", "Team 👍🏽"},
	{"the third review's set", "#\ufe0f\u20e3 1"},
	{"the third review's set", "🇨🇭 Rachith"},
	{"the third review's set", "🏳\ufe0f\u200d🌈 House"},
	{"the third review's set", "🧑🏽\u200d💻"},
	{"the third review's set", "❤\ufe0f\u200d🔥"},
	{"the third review's set", "🏳\ufe0f\u200d⚧\ufe0f"},
	{"the third review's set", "👁\ufe0f\u200d🗨\ufe0f"},
	{"the third review's set", "\U0001faf6🏾"},
	{"the third review's set", "☝🏻"},
	{"the third review's set", "🧑\u200d🧑\u200d🧒"},
	{"the third review's set", "👩🏽\u200d❤\ufe0f\u200d💋\u200d👨🏻"},
	{"the third review's set", "Arjun & Priya"},
	{"the third review's set", "Haus Nr. 7"},
	{"the third review's set", "©Home™"},
	{"the third review's set", "∞"},
	{"the third review's set", "Ⓐ"},
	{"the third review's set", "🙂"},
	{"the third review's set", "💩"},
	{"the third review's set", "日"},
	{"the third review's set", "A"},
	{"the third review's set", "7"},
	{"the third review's set", "Jürgen\u00a0Groß"},
	{"the third review's set", "Mary-Jane O'Hara-Smith"},
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

// A name is kept in the profile's form, and the page shows that form.
func TestNamesAreKeptNormalised(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, c := range []struct{ what, typed, kept string }{
		{"full-width letters", "Ａｒｊｕｎ", "Arjun"},
		{"ideographic spaces", "A\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000\u3000B", "A B"},
		{"an Ogham space mark between letters", "A\u1680B", "A B"},
		{"a no-break space", "Jürgen\u00a0Groß", "Jürgen Groß"},
		{"spaces around and between", "  Inês   Ferreira  ", "Inês Ferreira"},
		{"decomposed Korean jamo", "김민준", "김민준"},
		{"a circled letter", "Ⓐ", "A"},
		{"a trade mark sign", "©Home™", "©HomeTM"},
		{"a full-width household", "Ｔｈｅ\u3000Ｆｅｒｒｅｉｒａｓ", "The Ferreiras"},
	} {
		s := b.mustAnswer("household", map[string]string{"name": c.typed})
		if got := s.Answers["household"]; got != c.kept {
			t.Errorf("%s: %+q kept as %+q, want %+q", c.what, c.typed, got, c.kept)
		}
	}
}

// What doesn't show can't ride in a name, and a name that shows as blank is missing.
func TestNamesThatHideSomethingAreRefused(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, c := range []struct{ what, name, why string }{
		{"CGJ after letters x50", "Arjun\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f\u034f", "hidden-characters"},
		{"Khmer inherent vowels after letters", "Arjun\u17b4\u17b5\u17b4\u17b5", "hidden-characters"},
		{"unassigned DI E0080", "Arjun\U000e0080\U000e0081\U000e00ff", "hidden-characters"},
		{"unassigned DI E01F0", "Arjun\U000e01f0\U000e0fff", "hidden-characters"},
		{"unassigned DI U+2065", "Arjun\u2065", "hidden-characters"},
		{"unassigned DI U+FFF0", "Arjun\ufff0\ufff8", "hidden-characters"},
		{"ZWNJ/ZWJ binary payload 58", "Arjun\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d\u200c\u200d", "hidden-characters"},
		{"three ZWJ in a row", "Ar\u200d\u200d\u200djun", "hidden-characters"},
		{"null notehead alone", "\U0001d159", "hidden-characters"},
		{"null notehead x3", "\U0001d159\U0001d159\U0001d159", "hidden-characters"},
		{"enclosing mark alone", "\u20dd", "missing"},
		{"Cyrillic millions sign alone", "\u0488", "missing"},
		{"non-RGI flag ustx", "Tex 🏴\U000e0075\U000e0073\U000e0074\U000e0078\U000e007f", "hidden-characters"},
		{"bogus flag zzzzzz", "x 🏴\U000e007a\U000e007a\U000e007a\U000e007a\U000e007a\U000e007a\U000e007f", "hidden-characters"},
		{"7 flags x6 payload", "🏴\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e007f🏴\U000e0061\U000e006c\U000e006c\U000e0070\U000e0072\U000e0065\U000e007f🏴\U000e0076\U000e0069\U000e006f\U000e0075\U000e0073\U000e0069\U000e007f🏴\U000e006e\U000e0073\U000e0074\U000e0072\U000e0075\U000e0063\U000e007f🏴\U000e0074\U000e0069\U000e006f\U000e006e\U000e0073\U000e0061\U000e007f🏴\U000e006e\U000e0064\U000e0073\U000e0061\U000e0079\U000e0068\U000e007f", "hidden-characters"},
		{"bidi inside flag", "🏴\U000e0067\U000e0062\u202e\U000e0073\U000e0063\U000e0074\U000e007f", "hidden-characters"},
		{"RLM", "A\u200fB", "hidden-characters"},
		{"ALM", "A\u061cB", "hidden-characters"},
		{"FEFF mid", "A\ufeffB", "hidden-characters"},
		{"soft hyphen", "A\u00adB", "hidden-characters"},
		{"word joiner", "A\u2060B", "hidden-characters"},
		{"invisible times", "A\u2062B", "hidden-characters"},
		{"hangul filler alone", "\u3164", "hidden-characters"},
		{"hangul jungseong filler after letter", "A\u1160\u1160\u1160", "hidden-characters"},
		{"halfwidth hangul filler", "A\uffa0", "hidden-characters"},
		{"braille blank padding", "A\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800\u2800", "hidden-characters"},
		{"PUA", "A\ue000\uf8ff", "hidden-characters"},
		{"noncharacter", "A\uffff\ufdd0", "hidden-characters"},
		{"unassigned 0378", "A\u0378", "hidden-characters"},
		{"Egyptian format", "𓀀\U00013430𓀁", "hidden-characters"},
		{"interlinear annotation", "A\ufff9B\ufffb", "hidden-characters"},
		{"Mongolian FVS after MVS", "ᠠ\u180e\u180bᠠ", "hidden-characters"},
		{"Mongolian FVS x2", "ᠭ\u180b\u180c", "hidden-characters"},
		{"VS after mark", "e\u0301\ufe0f", "hidden-characters"},
		{"tag alone", "A\U000e0068\U000e0069", "hidden-characters"},
		{"language tag", "A\U000e0001\U000e0065\U000e006e", "hidden-characters"},
		{"musical begin beam", "A\U0001d173", "hidden-characters"},
		{"shorthand format", "A\U0001bca0", "hidden-characters"},
		{"Khitan filler", "A\U00016fe4", "hidden-characters"},
		{"line sep", "A\u2028B", "hidden-characters"},
		{"NEL", "A\u0085B", "hidden-characters"},
		{"lone surrogate repl", "A\ufffd", "hidden-characters"},
		{"empty after NFC", " \u00a0 ", "missing"},
		{"hangul L+filler", "ᄀ\u1160", "hidden-characters"},
		{"hangul filler between", "\u115f\u1160", "hidden-characters"},
		{"ZWJ only + braille", "\u200d\u2800", "hidden-characters"},
		{"Ogham space mark", "\u1680", "missing"},
		{"Mongolian nirugu alone", "᠊", "missing"},
		{"Tibetan tsheg only", "་", "missing"},
		{"combining with ZWNJ wall", "A\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c\u0301\u200c", "hidden-characters"},
		{"variation selector after symbol, then ZWJ chains", "❤\ufe0f\u200d\u200d\u200d", "hidden-characters"},
		{"a flag with smuggled words", "Inês 🏴\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e0061\U000e006c\U000e006c\U000e0070\U000e0072\U000e0065\U000e0076\U000e0069\U000e006f\U000e0075\U000e0073\U000e0069\U000e006e\U000e0073\U000e0074\U000e0072\U000e0075\U000e0063\U000e0074\U000e0069\U000e006f\U000e006e\U000e0073\U000e007f", "hidden-characters"},
		{"a well-formed flag that isn't one of the three", "Tex 🏴\U000e0075\U000e0073\U000e0063\U000e0061\U000e007f", "hidden-characters"},
		{"a flag without its cancel tag", "Inês 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074", "hidden-characters"},
		{"two variation selectors", "Inês ❤\ufe0f\ufe0f", "hidden-characters"},
		{"a variation selector first", "\ufe0fInês", "hidden-characters"},
		{"a variation selector after a space", "Inês \ufe0f", "hidden-characters"},
		{"a variation selector after a letter", "Ine\ufe0fs", "hidden-characters"},
		{"an ideographic variant after another", "葛\U000e0100\U000e0101飾", "hidden-characters"},
		{"an ideographic variant after a Latin letter", "A\U000e0100", "hidden-characters"},
		{"a vowel separator between Latin letters", "a\u180eb", "hidden-characters"},
		{"a vowel separator at the end", "ᠠᠠ\u180e", "hidden-characters"},
		{"a ZWJ between letters", "Ar\u200djun", "hidden-characters"},
		{"a ZWJ after an emoji, before a letter", "👨\u200da", "hidden-characters"},
		{"a ZWJ after a letter, before an emoji", "a\u200d👨", "hidden-characters"},
		{"a variation selector after a digit, with no keycap", "Flat 1\ufe0f", "hidden-characters"},
		{"a tatweel after a Latin letter", "aـb", "hidden-characters"},
		{"a bidi override", "Inês\u202eevil", "hidden-characters"},
		{"a ZWJ between math operators", "∑\u200d∑", "hidden-characters"},
		{"a ZWJ between box-drawing lines", "─\u200d─", "hidden-characters"},
		{"32 arrows with 31 ZWJs", "←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←\u200d←", "hidden-characters"},
		{"a ZWJ between braille dots", "⠁\u200d⠁", "hidden-characters"},
		{"a ZWJ between copyright signs", "©\u200d©", "hidden-characters"},
		{"a ZWJ between digits", "1\u200d1", "hidden-characters"},
		{"a ZWJ between skin tones", "🏻\u200d🏻", "hidden-characters"},
		{"a ZWJ before a skin tone", "😀\u200d🏻", "hidden-characters"},
		{"a skin tone after a copyright sign, then a ZWJ", "©🏽\u200d😀", "hidden-characters"},
		{"a skin tone after a smiley, then a ZWJ", "😀🏽\u200d😀", "hidden-characters"},
		{"a ZWJ before a keycap", "😀\u200d1\ufe0f\u20e3", "hidden-characters"},
		{"a ZWJ after a copyright sign without VS16", "©\u200d😀", "hidden-characters"},
		{"a ZWJ after a letter and a skin tone", "a🏻\u200d😀", "hidden-characters"},
		{"VS16 after 32 arrows", "←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f←\ufe0f", "hidden-characters"},
		{"VS16 after a math operator", "A ∑\ufe0f", "hidden-characters"},
		{"VS16 after a skin tone", "👍🏽\ufe0f", "hidden-characters"},
		{"an ideographic variant after the iteration mark", "々\U000e0100", "hidden-characters"},
		{"an ideographic variant after the ideographic zero", "〇\U000e0100", "hidden-characters"},
		{"an ideographic variant after a CJK radical", "⺀\U000e0100", "hidden-characters"},
		{"a tatweel starting an Arabic word", "ـمحمد", "hidden-characters"},
		{"a tatweel ending an Arabic word", "محمدـ", "hidden-characters"},
		{"an enclosing keycap alone", "\u20e3", "missing"},
		{"a ZWJ alone", "\u200d", "hidden-characters"},
		{"a ZWNJ alone", "\u200c", "hidden-characters"},
		{"a lone accent", "\u0301", "missing"},
		{"a combining grapheme joiner alone", "\u034f", "hidden-characters"},
		{"a braille blank", "\u2800", "hidden-characters"},
	} {
		r := b.answer("household", map[string]string{"name": c.name})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"name":"`+c.why+`"`) {
			t.Errorf("%s (%+q): %d %s, want 400 %s", c.what, c.name, r.Code, r.Body, c.why)
		}
	}
}

// A mail address: an ASCII local part as typed, or PRECIS's UsernameCaseMapped; the domain by IDNA's lookup profile;
// nothing invisible.
func TestMailAddresses(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	for _, c := range []struct{ what, typed, kept string }{
		{"a plain address", "arjun@rachith.ch", "arjun@rachith.ch"},
		{"dots, a tag and subdomains", "a.b+tag@sub.example.co.uk", "a.b+tag@sub.example.co.uk"},
		{"an apostrophe", "o'neil@example.ie", "o'neil@example.ie"},
		{"a non-ASCII local part", "inês@example.org", "inês@example.org"},
		{"an upper-case non-ASCII local part", "INÊS@example.org", "inês@example.org"},
		{"an upper-case ASCII local part, kept", "Ines@Example.ORG", "Ines@example.org"},
		{"an internationalised domain", "x@münchen.de", "x@münchen.de"},
		{"an internationalised domain in ASCII", "x@xn--mnchen-3ya.de", "x@münchen.de"},
		{"a local part of 64 octets", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@example.org", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@example.org"},
		{"a label of 63 octets", "a@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.com", "a@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.com"},
	} {
		r := b.answer("you", map[string]string{"name": "Inês", "mail": c.typed})
		var s state
		if r.Code == http.StatusOK {
			r.json(t, &s)
		}
		if r.Code != http.StatusOK || s.Answers["mail"] != c.kept {
			t.Errorf("%s: %+q: %d %s, want it kept as %+q", c.what, c.typed, r.Code, r.Body, c.kept)
		}
	}
	for _, c := range []struct{ what, typed, why string }{
		{"a ZWJ in the local part", "arj\u200dun@rachith.ch", "hidden-characters"},
		{"a ZWNJ in the domain", "arjun@rach\u200cith.ch", "hidden-characters"},
		{"a combining grapheme joiner after the domain", "arjun@rachith.ch\u034f", "hidden-characters"},
		{"a null notehead for a local part", "\U0001d159@x.ch", "hidden-characters"},
		{"a bidi override", "arjun\u202e@rachith.ch", "hidden-characters"},
		{"a domain whose A-label is a null notehead", "a@xn--ab-ep50a.com", "hidden-characters"},
		{"a domain whose A-label is a braille blank", "a@xn--ab-10y.com", "hidden-characters"},
		{"a domain whose A-label is the Khitan filler", "a@xn--ab-wl4u.com", "hidden-characters"},
		{"a domain whose A-label has a ZWNJ", "a@xn--ngbaaa2524a.com", "hidden-characters"},
		{"a domain whose A-label has a ZWJ", "a@xn--11b2ezcw70k.com", "hidden-characters"},
		{"a label of 64 octets", "a@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.com", "not-a-mail"},
		{"a local part of 65 octets", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@example.org", "too-long"},
		{"a domain that isn't one", "arjun@-x-.ch", "not-a-mail"},
		{"no dot in the domain", "arjun@localhost", "not-a-mail"},
		{"two at signs", "a@b@example.org", "not-a-mail"},
	} {
		r := b.answer("you", map[string]string{"name": "Inês", "mail": c.typed})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"mail":"`+c.why+`"`) {
			t.Errorf("%s (%+q): %d %s, want 400 %s", c.what, c.typed, r.Code, r.Body, c.why)
		}
	}
}

// Combining marks are capped at 4 in a row on one base (#124): "A" under 60 marks is Zalgo text, which draws over the
// lines around it. Real names with marks (Persian and Arabic vowel signs, Hebrew points, Thai, Hindi, Tibetan stacks)
// pass.
func TestCombiningMarksAreCapped(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	zalgo := "A" + strings.Repeat("\u0316\u0317\u0318\u0319\u031a", 12) // 60 marks that don't compose with the A
	for _, c := range []struct{ what, name string }{
		{"Persian with vowel signs", "محمّد رضا"},
		{"Persian with a shadda and a fatha", "علیٌّ \u0645\u064e\u0651"},
		{"Arabic with harakat", "عَبْدُ ٱللَّٰهِ"},
		{"Hebrew with niqqud and a dagesh", "ש\u05b8\u05c1לו\u05b9ם כ\u05b9\u05bcה\u05b5ן"},
		{"Thai with stacked marks", "ก\u0e38\u0e49 ส\u0e21\u0e0a\u0e32\u0e22\u0e48\u0e47"},
		{"a Tibetan stack of four marks", "བ\u0f71\u0f72\u0f74\u0f7a"},
		{"exactly four marks", "A\u0316\u0317\u0318\u0319"},
		{"four marks on each of two words", "A\u0316\u0317\u0318\u0319 B\u0316\u0317\u0318\u0319"},
		{"a keycap with its selector", "Flat 1\ufe0f\u20e3"},
	} {
		if r := b.answer("household", map[string]string{"name": c.name}); r.Code != http.StatusOK {
			t.Errorf("%s (%+q): %d %s, want it accepted", c.what, c.name, r.Code, r.Body)
		}
	}
	for _, c := range []struct{ what, name string }{
		{"60 marks on an A", zalgo},
		{"five marks on an A", "A\u0316\u0317\u0318\u0319\u031a"},
		{"five marks on a Persian letter", "م\u064b\u064c\u064d\u064e\u064f"},
		{"five marks on the second word", "Inês F\u0316\u0317\u0318\u0319\u031a"},
	} {
		r := b.answer("household", map[string]string{"name": c.name})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"name":"too-many-marks"`) {
			t.Errorf("%s as the household's name: %d %s, want 400 too-many-marks", c.what, r.Code, r.Body)
		}
		r = b.answer("you", map[string]string{"name": c.name, "mail": "ines@example.org"})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"name":"too-many-marks"`) {
			t.Errorf("%s as the admin's name: %d %s, want 400 too-many-marks", c.what, r.Code, r.Body)
		}
	}
}
