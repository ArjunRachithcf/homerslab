//go:build !compose57

package main

// versionSpecificTags: v5.5.x has no typed error for incompatible Up options (added in v5.7.0).
func versionSpecificTags(error) []string { return nil }
