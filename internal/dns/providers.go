package dns

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/libdns/infomaniak"
	"github.com/libdns/rfc2136"
	mdns "github.com/miekg/dns"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Settings say which DNS provider holds the household's zone, and how to reach it. None of it is secret: the token
// (an API token, or RFC 2136's TSIG key) is sealed on its own, and given only in this machine's terminal (#64).
type Settings struct {
	Provider string `json:"provider"`
	// RFC 2136 (BIND, Knot, PowerDNS and others): the server, host:port, and the TSIG key's name and algorithm.
	Server       string `json:"server,omitempty"`
	KeyName      string `json:"keyName,omitempty"`
	KeyAlgorithm string `json:"keyAlgorithm,omitempty"`
}

// Providers are the DNS providers this version of HomersLab speaks, by name.
var Providers = map[string]string{
	"infomaniak": "Infomaniak",
	"rfc2136":    "RFC 2136 (BIND, Knot, PowerDNS)",
}

var tsigAlgorithms = map[string]string{
	"hmac-sha256": mdns.HmacSHA256, "hmac-sha384": mdns.HmacSHA384, "hmac-sha512": mdns.HmacSHA512,
}

var keyName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

// Invalid names the settings that don't hold.
type Invalid struct{ Fields []string }

func (e *Invalid) Error() string { return "invalid DNS settings: " + strings.Join(e.Fields, ", ") }

// Check says which settings don't hold, and returns them tidied.
func (s Settings) Check() (Settings, error) {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if _, ok := Providers[s.Provider]; !ok {
		return s, &Invalid{Fields: []string{"provider"}}
	}
	if s.Provider != "rfc2136" {
		return Settings{Provider: s.Provider}, nil
	}
	var bad []string
	s.Server = strings.TrimSpace(s.Server)
	host, port, err := net.SplitHostPort(s.Server)
	if err != nil {
		host, port = s.Server, "53"
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 || host == "" || strings.ContainsAny(host, "/ ") {
		bad = append(bad, "server")
	} else {
		s.Server = net.JoinHostPort(host, port)
	}
	s.KeyName = strings.ToLower(strings.TrimSpace(s.KeyName))
	if !keyName.MatchString(s.KeyName) {
		bad = append(bad, "keyName")
	}
	s.KeyAlgorithm = strings.ToLower(strings.TrimSpace(s.KeyAlgorithm))
	if s.KeyAlgorithm == "" {
		s.KeyAlgorithm = "hmac-sha256"
	}
	if _, ok := tsigAlgorithms[s.KeyAlgorithm]; !ok {
		bad = append(bad, "keyAlgorithm")
	}
	if bad != nil {
		return s, &Invalid{Fields: bad}
	}
	return s, nil
}

// Where says where the token goes, in plain words, for the terminal to show before it is read (#64).
func (s Settings) Where() string {
	if s.Provider == "rfc2136" {
		return "the DNS server " + s.Server + " (RFC 2136), as the key " + s.KeyName
	}
	return Providers[s.Provider] + "'s API"
}

// Open is the provider the settings name, with its token.
func Open(s Settings, token mesh.Secret) (Provider, error) {
	s, err := s.Check()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.Reveal()) == "" {
		return nil, errors.New("no DNS token")
	}
	switch s.Provider {
	case "infomaniak":
		return &infomaniak.Provider{APIToken: token.Reveal()}, nil
	case "rfc2136":
		return &rfc2136.Provider{Server: s.Server, KeyName: s.KeyName, KeyAlg: tsigAlgorithms[s.KeyAlgorithm], Key: token.Reveal()}, nil
	}
	return nil, &Invalid{Fields: []string{"provider"}}
}
