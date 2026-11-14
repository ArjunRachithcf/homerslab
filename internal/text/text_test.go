package text_test

import (
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
	"github.com/ArjunRachithcf/homerslab/internal/text"
)

func TestANameIsOneLineOfPrintableText(t *testing.T) {
	testenv.Isolate(t)
	for _, ok := range []string{"BentoPDF", "Immich", "Gramps Web", "Café ☕", "Paperless-ngx", "家族の写真"} {
		if !text.OneLine(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for name, bad := range map[string]string{
		"a newline": "X\nservices:", "a carriage return": "X\r", "NUL": "X\x00", "a tab": "X\tY", "DEL": "X\x7f",
		"NEL (YAML's line break)":  "X\u0085---\u0085services: {web: {privileged: true}}\u0085---\u0085#",
		"the line separator":       "X include: [/etc/shadow]",
		"the paragraph separator":  "X Y",
		"a right-to-left override": "‮FDPotneB",
		"an isolate":               "X⁦Y⁩",
		"not UTF-8":                "X\xffY",
	} {
		if text.OneLine(bad) {
			t.Errorf("%s: %q taken", name, bad)
		}
	}
}
