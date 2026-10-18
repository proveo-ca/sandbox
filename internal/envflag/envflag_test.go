// SPEC: _spec/internal/envflag/env-switches.puml
package envflag

import "testing"

func TestOnOffBoolVocabulary(t *testing.T) {
	t.Parallel()
	type want struct{ on, off, defTrue, defFalse bool }
	onWant := want{on: true, defTrue: true, defFalse: true}
	offWant := want{off: true}
	unknown := want{defTrue: true}
	cases := map[string]want{
		"1": onWant, "true": onWant, "yes": onWant, "on": onWant, "enable": onWant, "enabled": onWant,
		"TRUE": onWant, "Yes": onWant, "ON": onWant, "Enabled": onWant, " 1 ": onWant, "\ton\n": onWant,
		"0": offWant, "false": offWant, "no": offWant, "off": offWant, "disable": offWant, "disabled": offWant,
		"FALSE": offWant, "No": offWant, "OFF": offWant, "Disabled": offWant, " 0 ": offWant, "\toff\n": offWant,
		"": unknown, " ": unknown, "2": unknown, "y": unknown, "n": unknown, "auto": unknown,
		"always": unknown, "never": unknown, "none": unknown, "maybe": unknown, "o n": unknown, "-1": unknown,
	}
	for v, w := range cases {
		if got := On(v); got != w.on {
			t.Errorf("On(%q) = %v, want %v", v, got, w.on)
		}
		if got := Off(v); got != w.off {
			t.Errorf("Off(%q) = %v, want %v", v, got, w.off)
		}
		if got := Bool(v, true); got != w.defTrue {
			t.Errorf("Bool(%q, true) = %v, want %v", v, got, w.defTrue)
		}
		if got := Bool(v, false); got != w.defFalse {
			t.Errorf("Bool(%q, false) = %v, want %v", v, got, w.defFalse)
		}
	}
}
