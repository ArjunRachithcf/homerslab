package bidi_test

import (
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/bidi"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A name shown in text is wrapped in a first-strong isolate (U+2068 ... U+2069, UAX #9), so its direction doesn't
// reorder the words around it, and the words around it don't reorder it.
func TestANameIsIsolated(t *testing.T) {
	testenv.Isolate(t)
	for typed, want := range map[string]string{
		"Inês Ferreira": "⁨Inês Ferreira⁩",
		"محمد رضا":      "⁨محمد رضا⁩",
		"שלום":          "⁨שלום⁩",
		"":              "",
	} {
		if got := bidi.Isolate(typed); got != want {
			t.Errorf("Isolate(%+q) = %+q, want %+q", typed, got, want)
		}
	}
}

func TestEachNameInAListIsIsolated(t *testing.T) {
	testenv.Isolate(t)
	if got, want := bidi.Join([]string{"محمد", "Rui"}, ", "), "⁨محمد⁩, ⁨Rui⁩"; got != want {
		t.Errorf("Join = %+q, want %+q", got, want)
	}
}
