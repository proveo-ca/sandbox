//go:build e2e

// SPEC: _spec/internal/sbx/state-sync.puml
package e2e

import (
	"strings"
	"testing"
	"time"
)

// A state sync must never leave a partially written file at the destination.
//
// The `cp -a "$src/." "$dst/"` this replaced truncates in place — cp opens the
// destination with O_TRUNC and streams into it — so anything that interrupts the
// copy leaves the destination cut at a read-buffer boundary. proveo's own failure
// path did the interrupting: `sbx exec` on a stopped sandbox restarts it, so the
// seed's `restore` ran concurrently with the exec's `save`, in the opposite
// direction over the same files. Seven of the operator's transcripts were rewritten
// short inside one second — 7340032, 786432, 786432, 524288, 262144, 262144 and
// 262144 bytes, every size an exact 256 KiB multiple, each cut mid-JSON — and the
// short copy was propagated to both sides. Nothing reported it.
//
// The assertion samples the destination's size WHILE a copy is running. With a
// rename into place there are only two possible sizes, the old and the new; with a
// streaming overwrite the samples land in between. That is the property, stated so
// it fails against the old implementation rather than merely describing the new one.
func TestSyncTreeNeverExposesAPartialFile(t *testing.T) {
	out, err := runLib(t, runOpts{timeout: 4 * time.Minute}, `
set -u
src=$(mktemp -d) dst=$(mktemp -d)
# Two sizes far enough apart that any intermediate sample is unambiguous, and big
# enough that the copy is still running when the sampler looks.
head -c 40000000 /dev/urandom > "$src/big.jsonl"
head -c  4000000 /dev/urandom > "$dst/big.jsonl"
want_new=$(stat -c %s "$src/big.jsonl")
want_old=$(stat -c %s "$dst/big.jsonl")

_proveo_sync_tree "$src" "$dst" &
copier=$!
bad=0 samples=0
while kill -0 "$copier" 2>/dev/null; do
  sz=$(stat -c %s "$dst/big.jsonl" 2>/dev/null || echo "$want_old")
  samples=$((samples + 1))
  if [ "$sz" != "$want_new" ] && [ "$sz" != "$want_old" ]; then
    bad=$((bad + 1))
    printf 'PARTIAL size=%s\n' "$sz"
  fi
done
wait "$copier"; rc=$?
printf 'samples=%s partial=%s rc=%s\n' "$samples" "$bad" "$rc"
printf 'final=%s\n' "$(stat -c %s "$dst/big.jsonl")"
printf 'wantsize=%s\n' "$want_new"
# An interrupted copy must leave no debris behind either.
printf 'temps=%s\n' "$(find "$dst" -name '*.proveo-sync.*' | wc -l | tr -d ' ')"
`)
	if err != nil {
		t.Fatalf("sync_tree probe failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "PARTIAL size=") {
		t.Errorf("the destination was observed mid-write; a reader would have seen a truncated file:\n%s", out)
	}
	if !strings.Contains(out, "partial=0") {
		t.Errorf("want no partial observations:\n%s", out)
	}
	if !strings.Contains(out, "rc=0") {
		t.Errorf("the copy reported failure:\n%s", out)
	}
	if !strings.Contains(out, "temps=0") {
		t.Errorf("temp files were left in the destination:\n%s", out)
	}
	// The copy still has to have HAPPENED — a no-op leaves no partial file either.
	fields := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			fields[k] = v
		}
	}
	if fields["final"] == "" || fields["final"] != fields["wantsize"] {
		t.Errorf("the destination did not end up holding the source: final=%q want=%q\n%s",
			fields["final"], fields["wantsize"], out)
	}
}

// New files take the fast path, and it must not be able to destroy anything.
//
// Phase 1 is one `cp -an` for everything the destination lacks — no existing file is
// opened for writing at all. Phase 2 is the only place an existing file is replaced,
// and it replaces by rename. Unchanged files are skipped outright, which is what
// keeps a warm sandbox cheap: the plugin marketplace alone is thousands of files
// that never differ, and a fork per file was the reason not to do this before.
func TestSyncTreePlacesNewFilesAndLeavesTheDestinationsOwnAlone(t *testing.T) {
	out, err := runLib(t, runOpts{timeout: 4 * time.Minute}, `
set -u
src=$(mktemp -d) dst=$(mktemp -d)
mkdir -p "$src/projects/-a" "$dst/projects/-a" "$dst/projects/-b"
printf 'from-source\n'      > "$src/projects/-a/new.jsonl"
printf 'only-the-host-has\n' > "$dst/projects/-b/theirs.jsonl"
printf 'stale\n'            > "$dst/projects/-a/shared.jsonl"
printf 'fresh\n'            > "$src/projects/-a/shared.jsonl"
# Identical on both sides, mtime included: cp -a preserves mtime, so this is what an
# already-copied file looks like and it must not be touched. Back-dated out of the
# grace window, because a stamp is only trusted as an identity once it has stopped
# being "now" — see _proveo_changed_files.
printf 'identical\n' > "$src/projects/-a/same.jsonl"
cp -a "$src/projects/-a/same.jsonl" "$dst/projects/-a/same.jsonl"
touch -d '2 hours ago' "$src/projects/-a/same.jsonl" "$dst/projects/-a/same.jsonl"
before=$(stat -c '%s %Y %i' "$dst/projects/-a/same.jsonl")
# Inside the grace window the stamps are not trusted: on this overlay two files
# written in one clock tick report the SAME nanosecond mtime, so a just-written file
# is copied whatever its stamps claim. These two are the same size on purpose.
printf 'AAAAA\n' > "$dst/projects/-a/hot.jsonl"
printf 'BBBBB\n' > "$src/projects/-a/hot.jsonl"
touch -r "$dst/projects/-a/hot.jsonl" "$src/projects/-a/hot.jsonl"

_proveo_sync_tree "$src" "$dst"; printf 'rc=%s\n' "$?"
printf 'new=%s\n'    "$(cat "$dst/projects/-a/new.jsonl")"
printf 'shared=%s\n' "$(cat "$dst/projects/-a/shared.jsonl")"
printf 'theirs=%s\n' "$(cat "$dst/projects/-b/theirs.jsonl")"
printf 'untouched=%s\n' "$([ "$before" = "$(stat -c '%s %Y %i' "$dst/projects/-a/same.jsonl")" ] && echo yes || echo no)"
printf 'hot=%s\n' "$(cat "$dst/projects/-a/hot.jsonl")"
`)
	if err != nil {
		t.Fatalf("sync_tree probe failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"rc=0",
		"new=from-source",          // placed by the no-clobber pass
		"shared=fresh",             // replaced by rename
		"theirs=only-the-host-has", // a sync has never deleted
		"untouched=yes",            // a settled, already-copied file is skipped, inode and all
		"hot=BBBBB",                // and a just-written one is copied even with matching stamps
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

// restore and save move the same files in opposite directions, and proveo's failure
// path runs both at once without meaning to. The lock is what makes the second
// caller wait rather than race; a stale one — a seed killed mid-copy — must be
// broken rather than blocking every later run forever.
func TestSyncLockSerialisesAndBreaksWhenTheHolderIsGone(t *testing.T) {
	out, err := runLib(t, runOpts{timeout: 3 * time.Minute}, `
set -u
lock=$(mktemp -d)/sync.lock
_proveo_sync_lock "$lock" && printf 'first=held\n'
# A second acquisition while a LIVE holder has it must not succeed. The holder here
# is this shell, so the pid in the lock is alive by construction.
( _proveo_sync_lock "$lock" && printf 'second=held\n' || printf 'second=refused\n' ) &
sleep 1
kill %1 2>/dev/null || true
wait 2>/dev/null || true
printf 'still-locked=%s\n' "$([ -d "$lock" ] && echo yes || echo no)"

# Stale: the recorded holder is gone, so the lock is debris and must be broken.
printf '999999\n' > "$lock/pid"
_proveo_sync_lock "$lock" && printf 'stale=broken\n' || printf 'stale=blocked\n'
rm -rf "$lock"
`)
	if err != nil {
		t.Fatalf("sync_lock probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "first=held") {
		t.Errorf("the first caller must take the lock:\n%s", out)
	}
	if strings.Contains(out, "second=held") {
		t.Errorf("two syncs held the lock at once — restore and save can still collide:\n%s", out)
	}
	if !strings.Contains(out, "stale=broken") {
		t.Errorf("a lock whose holder is gone must be broken, not waited on:\n%s", out)
	}
}

// A failed state sync must never stop a sandbox from coming up, and this is a
// regression that actually happened while fixing the one above.
//
// The old copy ended in `|| true` and could not report anything. Giving
// proveo_sync_state an honest exit status — needed so `save`'s caller and these
// tests can see a partial copy — put that status inside proveo_seed, which
// proveo-seed runs under `set -euo pipefail`. One failed file copy then aborted the
// kit's startup command and sbx answered `failed to run sandbox container`: no
// sandbox at all, for the sake of a transcript that did not copy.
//
// So the status stays honest and the SEED stays tolerant, and this pins the seam.
// The sync is replaced with one that fails, everything after it is stubbed, and the
// seed still has to return 0 having gone past it.
func TestSeedSurvivesAFailedStateSync(t *testing.T) {
	out, err := runLib(t, runOpts{timeout: 3 * time.Minute}, `
set -euo pipefail
# Fails the way a real copy error does: non-zero, with a word on stderr.
proveo_sync_state() { printf 'proveo: state restore completed with copy errors\n' >&2; return 1; }
render_subagents() { printf 'REACHED=subagents\n'; }
accept_workspace_trust() { :; }
proveo_provision_toolchain() { :; }
configure_claude_lsp() { :; }
proveo_compose_house_rules() { :; }
proveo_apply_ui_defaults() { printf 'REACHED=end\n'; }
proveo_seed claudecode
printf 'seed-rc=%s\n' "$?"
`)
	if err != nil {
		t.Fatalf("the seed aborted on a failed sync — no sandbox would come up: %v\n%s", err, out)
	}
	for _, want := range []string{"REACHED=subagents", "REACHED=end", "seed-rc=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q — the seed must continue past a failed sync; got:\n%s", want, out)
		}
	}
	// The failure is still SAID. Silence is what let a copy that destroyed seven
	// files look like a copy that worked.
	if !strings.Contains(out, "copy errors") {
		t.Errorf("a failed sync must still be reported, got:\n%s", out)
	}
}
