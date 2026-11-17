package dns

import (
	"context"

	mdns "github.com/miekg/dns"
)

// WithExchange is r sending its questions through f: a test's network, where some addresses can't be reached.
func WithExchange(r Resolver, f func(ctx context.Context, q *mdns.Msg, server string) (*mdns.Msg, error)) Resolver {
	r.exchange = f
	return r
}

// Quoted is quoted: a TXT value in its presentation form, read as one text.
var Quoted = quoted
