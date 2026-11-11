// Package plantcall calls a vulnerable function: x/text v0.3.7's language.ParseAcceptLanguage (GO-2022-1059).
package plantcall

import "golang.org/x/text/language"

func Parse(s string) error { _, _, err := language.ParseAcceptLanguage(s); return err }
