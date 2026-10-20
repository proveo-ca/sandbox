// SPEC: _spec/internal/agentio/agent-terminal-io.puml
package termtext

import "testing"

func TestStripANSIRemovesEverySequenceAndKeepsTheText(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, in, want string }{
		{"sgr", "\x1b[1;32mok\x1b[0m", "ok"},
		{"a non-SGR sequence before text holding an m", "\x1b[2Khello mom", "hello mom"},
		{"private csi", "\x1b[?25l\x1b[>4;1mx", "x"},
		{"osc title, BEL", "\x1b]0;title\x07body", "body"},
		{"osc link, ST", "\x1b]8;;https://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"dcs", "\x1bPq#0\x1b\\after", "after"},
		{"charset", "\x1b(Bplain", "plain"},
		{"keypad and save cursor", "\x1b=\x1b7a\x1b8", "a"},
		{"lone escape at the end", "tail\x1b", "tail"},
	} {
		if got := StripANSI(c.in); got != c.want {
			t.Errorf("%s: StripANSI(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestWordsReadsEachSequenceAsASpace(t *testing.T) {
	t.Parallel()
	if got := Words("a\x1b[1Cb\r\n  \x1b[1mc\x1b[0m"); got != "a b c" {
		t.Errorf("Words = %q, want %q", got, "a b c")
	}
}
