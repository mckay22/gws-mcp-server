package main

import (
	"errors"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
)

// Result-size bounds shared by the list/search tools.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// clampLimit bounds a caller-supplied result cap to [1, maxLimit], defaulting a
// non-positive value to defaultLimit.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	default:
		return n
	}
}

// toolError surfaces a Google API failure to the caller: when the client returns
// a *gapi.Error, its human-readable Message becomes the tool error; any other
// error is passed through unchanged. The bearer token and request body are never
// part of a *gapi.Error, so nothing sensitive leaks.
func toolError(err error) error {
	var ge *gapi.Error
	if errors.As(err, &ge) && ge.Message != "" {
		return errors.New(ge.Message)
	}
	return err
}
