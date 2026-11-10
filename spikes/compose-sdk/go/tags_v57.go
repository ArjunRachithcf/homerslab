//go:build compose57

package main

import (
	"errors"

	"github.com/docker/compose/v5/pkg/api"
)

// versionSpecificTags: v5.7.0 validates Up's options first and returns *api.IncompatibleOptionsError.
func versionSpecificTags(err error) []string {
	var tags []string
	var inc *api.IncompatibleOptionsError
	if errors.As(err, &inc) {
		tags = append(tags, "api.IncompatibleOptionsError options="+joinOpts(inc.Options))
	}
	if errors.Is(err, api.ErrIncompatibleOptions) {
		tags = append(tags, "api.ErrIncompatibleOptions")
	}
	return tags
}

func joinOpts(o []string) string {
	s := ""
	for i, x := range o {
		if i > 0 {
			s += ","
		}
		s += x
	}
	return s
}
