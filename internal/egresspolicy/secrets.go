// SPEC: _spec/internal/egresspolicy/egress-policy-decide.puml,
// _spec/internal/egresspolicy/egress-policy-layers.puml,
// _spec/_conventions/design-decision-ids.puml
//
// SPEC: _spec/internal/egresspolicy/egress-policy-decide.puml, _spec/internal/egresspolicy/egress-policy-layers.puml, _spec/_conventions/design-decision-ids.puml
package egresspolicy

import (
	"encoding/base64"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

const minSecretLen = 8

const (
	entropyMinTokenLen = 24
	entropyBitsPerChar = 4.0
)

var knownSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),         // OpenAI / Anthropic sk-ant-…
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),              // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),    // GitHub tokens
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),  // Slack tokens
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`), // PEM private keys
}

var (
	reB64URL = regexp.MustCompile(`[A-Za-z0-9_-]{12,}`)
	reB64Std = regexp.MustCompile(`[A-Za-z0-9+/]{12,}={0,2}`)
	reHex    = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
)

type scanner struct {
	secrets  []string // exact values (len >= minSecretLen), matched case-sensitively
	patterns bool
	decode   bool
	entropy  bool
}

func newScanner(secrets []string, patterns, decode, entropy bool) *scanner {
	s := &scanner{patterns: patterns, decode: decode, entropy: entropy}
	for _, v := range secrets {
		if len(strings.TrimSpace(v)) >= minSecretLen {
			s.secrets = append(s.secrets, v)
		}
	}
	return s
}

func (s *scanner) active() bool {
	return len(s.secrets) > 0 || s.patterns || s.entropy
}

func (s *scanner) hit(hay string) bool {
	if hay == "" {
		return false
	}
	if s.matchKnown(hay) {
		return true
	}
	if s.decode && s.decodedHit(hay) {
		return true
	}
	return s.entropy && hasHighEntropyToken(hay)
}

func (s *scanner) matchKnown(hay string) bool {
	for _, v := range s.secrets {
		if strings.Contains(hay, v) {
			return true
		}
	}
	if s.patterns {
		for _, re := range knownSecretPatterns {
			if re.MatchString(hay) {
				return true
			}
		}
	}
	return false
}

func (s *scanner) decodedHit(hay string) bool {
	for _, m := range reB64URL.FindAllString(hay, -1) {
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(m, "=")); err == nil && s.matchDecoded(b) {
			return true
		}
	}
	for _, m := range reB64Std.FindAllString(hay, -1) {
		if b, err := base64.StdEncoding.DecodeString(m); err == nil && s.matchDecoded(b) {
			return true
		}
		if b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(m, "=")); err == nil && s.matchDecoded(b) {
			return true
		}
	}
	for _, m := range reHex.FindAllString(hay, -1) {
		if len(m)%2 == 1 {
			m = m[:len(m)-1]
		}
		if b, err := hex.DecodeString(m); err == nil && s.matchDecoded(b) {
			return true
		}
	}
	return false
}

func (s *scanner) matchDecoded(b []byte) bool {
	return len(b) >= minSecretLen && isMostlyPrintable(b) && s.matchKnown(string(b))
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	printable := 0
	for _, c := range b {
		if c == '\t' || c == '\n' || c == '\r' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*10 >= len(b)*9
}

func hasHighEntropyToken(hay string) bool {
	for _, tok := range tokenize(hay) {
		if len(tok) >= entropyMinTokenLen && mixedClasses(tok) && shannon(tok) >= entropyBitsPerChar {
			return true
		}
	}
	return false
}

func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '+' || r == '/' || r == '=' || r == '_' || r == '-':
			return false
		}
		return true
	})
}

func mixedClasses(s string) bool {
	var hasLetter, hasDigit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	return hasLetter && hasDigit
}

func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
