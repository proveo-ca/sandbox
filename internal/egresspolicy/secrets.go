// SPEC: _spec/internal/egresspolicy/egress-policy-decide.puml, _spec/internal/egresspolicy/egress-policy-layers.puml
package egresspolicy

import (
	"encoding/base64"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

// minSecretLen ignores exact-match values too short to be a real credential (and
// prone to false positives).
const minSecretLen = 8

// entropy heuristic thresholds: a contiguous token must be at least this long,
// carry both letters and digits, and exceed this Shannon entropy to be flagged.
const (
	entropyMinTokenLen = 24
	entropyBitsPerChar = 4.0
)

// knownSecretPatterns match common credential shapes regardless of value.
var knownSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),         // OpenAI / Anthropic sk-ant-…
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),              // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),    // GitHub tokens
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),  // Slack tokens
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`), // PEM private keys
}

// Candidate encoded runs for decode-and-rescan. Minimum lengths avoid decoding
// every short word (base64 of an 8-byte secret is ~11-12 chars; hex is 2x the
// bytes). "-" is last in the class so it stays literal. Extracting per-encoding
// runs — rather than reusing the entropy tokenizer, which keeps "/","+","=" and
// so glues URL delimiters onto tokens — is what lets `/<base64url>` decode.
var (
	reB64URL = regexp.MustCompile(`[A-Za-z0-9_-]{12,}`)
	reB64Std = regexp.MustCompile(`[A-Za-z0-9+/]{12,}={0,2}`)
	reHex    = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
)

// scanner detects secrets in a text haystack via four optional detectors:
// exact user secret values, generic credential-shape patterns, decode-and-rescan
// of base64/hex tokens (the primary counter to encoding evasion), and a
// high-entropy-token heuristic (an opt-in backstop for unknown encoded blobs).
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

// active reports whether any detector can fire. decode is a modifier on the
// exact/pattern matchers, so it does not activate the scanner on its own.
func (s *scanner) active() bool {
	return len(s.secrets) > 0 || s.patterns || s.entropy
}

// hit reports whether hay carries a secret per the enabled detectors.
func (s *scanner) hit(hay string) bool {
	if hay == "" {
		return false
	}
	if s.matchKnown(hay) {
		return true
	}
	// Primary encoding-evasion defense: a base64/hex token that DECODES to a
	// known secret or credential shape. Unlike the entropy heuristic this only
	// fires on tokens that decode to an ACTUAL secret, so it does not flag benign
	// high-entropy URLs (S3 presigned links, JWTs, content hashes).
	if s.decode && s.decodedHit(hay) {
		return true
	}
	return s.entropy && hasHighEntropyToken(hay)
}

// matchKnown reports whether hay directly contains a known exact secret value or
// a generic credential-shape pattern (no decoding, no entropy).
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

// decodedHit reports whether any base64/hex-encoded run in hay decodes (one
// level) to a value carrying a known secret or credential shape. Runs are
// extracted per-encoding so URL delimiters ("/", "?", "=", "&") that border a
// token don't defeat the decode.
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

// matchDecoded runs the exact/pattern matchers on decoded bytes, ignoring
// decodings too short or clearly binary — a benign random signature decodes to
// non-text bytes and is dropped rather than scanned (no false positive).
func (s *scanner) matchDecoded(b []byte) bool {
	return len(b) >= minSecretLen && isMostlyPrintable(b) && s.matchKnown(string(b))
}

// isMostlyPrintable reports whether b is >=90% printable ASCII/whitespace — the
// shape of a decoded credential, not of random bytes from a benign signature.
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

// hasHighEntropyToken reports whether hay contains a long, mixed, high-entropy
// token — the shape of an encoded credential, not of ordinary prose or paths.
func hasHighEntropyToken(hay string) bool {
	for _, tok := range tokenize(hay) {
		if len(tok) >= entropyMinTokenLen && mixedClasses(tok) && shannon(tok) >= entropyBitsPerChar {
			return true
		}
	}
	return false
}

// tokenize splits on characters outside a credential-ish alphabet, so slugs and
// prose break into short tokens while base64/hex secrets stay contiguous.
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

// shannon returns the Shannon entropy of s in bits per byte.
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
