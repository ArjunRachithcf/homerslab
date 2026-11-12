package certs

import (
	"context"

	"github.com/mholt/acmez/v3"
	"github.com/mholt/acmez/v3/acme"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
)

// Solver is the DNS challenge's solver Issue gives the ACME client, with the wait for its record's propagation.
type Solver interface {
	acmez.Solver
	Wait(ctx context.Context, ch acme.Challenge) error
}

// ChallengeSolver is the DNS challenge's solver Issue gives the ACME client for a zone.
func ChallengeSolver(a Authority, zone *dns.Owned) Solver { return &solver{a: a, zone: zone} }
