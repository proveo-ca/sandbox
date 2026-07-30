package shell

import "testing"

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in            string
		wantName      string
		wantOK        bool
		wantSupported bool
	}{
		{"/bin/bash", "bash", true, true},
		{"/usr/bin/zsh", "zsh", true, true},
		{"/opt/homebrew/bin/fish", "fish", true, true},
		{"/bin/sh", "sh", true, true},
		{"/bin/tcsh", "tcsh", true, false},
		{"/usr/bin/elvish", "", false, false},
		{"", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, ok := Detect(tc.in)
			if ok != tc.wantOK || got.Name != tc.wantName || got.Supported != tc.wantSupported {
				t.Errorf("Detect(%q) = (%+v, %v), want name=%q supported=%v ok=%v",
					tc.in, got, ok, tc.wantName, tc.wantSupported, tc.wantOK)
			}
		})
	}
}

func TestRCFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		shell, goos, want string
	}{
		{"bash", "darwin", "/home/u/.bash_profile"},
		{"bash", "linux", "/home/u/.bashrc"},
		{"zsh", "linux", "/home/u/.zshrc"},
		{"fish", "linux", "/home/u/.config/fish/config.fish"},
		{"sh", "linux", "/home/u/.profile"},
		{"ksh", "linux", "/home/u/.profile"},
	}
	for _, tc := range tests {
		t.Run(tc.shell+"/"+tc.goos, func(t *testing.T) {
			t.Parallel()
			s := known[tc.shell]
			if got := s.RCFile(tc.goos, "/home/u"); got != tc.want {
				t.Errorf("%s.RCFile(%s) = %q, want %q", tc.shell, tc.goos, got, tc.want)
			}
		})
	}
}

func TestPathLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		shell, want string
	}{
		{"bash", `export PATH="/opt/bin:$PATH"`},
		{"zsh", `export PATH="/opt/bin:$PATH"`},
		{"fish", `set -gx PATH "/opt/bin" $PATH`},
		{"tcsh", `setenv PATH "/opt/bin:$PATH"`},
	}
	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			if got := known[tc.shell].PathLine("/opt/bin"); got != tc.want {
				t.Errorf("%s.PathLine = %q, want %q", tc.shell, got, tc.want)
			}
		})
	}
}

func TestExportLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		shell, want string
	}{
		{"bash", `export FOO="bar"`},
		{"zsh", `export FOO="bar"`},
		{"fish", `set -gx FOO "bar"`},
		{"tcsh", `setenv FOO "bar"`},
	}
	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			if got := known[tc.shell].ExportLine("FOO", "bar"); got != tc.want {
				t.Errorf("%s.ExportLine = %q, want %q", tc.shell, got, tc.want)
			}
		})
	}
}

func TestAlreadyConfigured(t *testing.T) {
	t.Parallel()
	bin := "/home/u/.local/bin"
	if !AlreadyConfigured("\n"+Marker+"\nexport PATH=...\n", bin) {
		t.Error("marker present should be already-configured")
	}
	if !AlreadyConfigured(`export PATH="/home/u/.local/bin:$PATH"`, bin) {
		t.Error("binDir present should be already-configured")
	}
	if AlreadyConfigured("export PATH=/usr/bin:$PATH", bin) {
		t.Error("unrelated rc should not be already-configured")
	}
}
