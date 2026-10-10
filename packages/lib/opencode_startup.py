# SPEC: _spec/packages/lib/opencode-seed-progress.puml

import contextlib
import os
from pathlib import Path
import sys
import tempfile
import time


STATUS_FILE = "/dev/shm/proveo-seed-status"


class SeedWait:
    def __init__(
        self, env, fallback="starting: waiting for instruction seed", stream=None
    ):
        self.enabled = env.get("PROVEO_SEED_PROGRESS") == "1"
        self.path = Path(env.get("PROVEO_SEED_STATUS_FILE", STATUS_FILE))
        self.fallback = fallback
        self.stream = stream if stream is not None else sys.stderr
        self.terminal = self.stream.isatty() and env.get("TERM") != "dumb"
        self.started = time.monotonic()
        self.last_update = None
        self.last_label = None
        self.frame = 0

    def __enter__(self):
        return self

    def update(self):
        if not self.enabled:
            return
        now = time.monotonic()
        if self.last_update is not None and now - self.last_update < 0.2:
            return
        try:
            label = self.path.read_text().strip() or self.fallback
        except (OSError, UnicodeError):
            label = self.fallback
        elapsed = int(now - self.started)
        if self.terminal:
            frame = "|/-\\"[self.frame % 4]
            self.frame += 1
            self.stream.write(
                f"\r\x1b[2Kproveo: Waiting for seed — {label} ({elapsed}s) {frame}"
            )
        elif label != self.last_label or now - self.last_update >= 5:
            self.stream.write(f"proveo: Waiting for seed — {label} ({elapsed}s)\n")
        else:
            return
        self.stream.flush()
        self.last_update = now
        self.last_label = label

    def __exit__(self, *_):
        if self.enabled and self.terminal and self.last_update is not None:
            self.stream.write("\r\x1b[2K")
            self.stream.flush()


class StartupSteps:
    def __init__(self, env, publish=False):
        self.enabled = publish and env.get("PROVEO_SEED_PROGRESS") == "1"
        self.path = Path(env.get("PROVEO_SEED_STATUS_FILE", STATUS_FILE))

    @contextlib.contextmanager
    def step(self, category, detail):
        label = f"{category}: {detail}"
        if self.enabled:
            print(f"proveo-seed: {label}", file=sys.stderr, flush=True)
            temporary = None
            try:
                with tempfile.NamedTemporaryFile(
                    mode="w",
                    prefix=".proveo-seed-status-",
                    dir=self.path.parent,
                    delete=False,
                ) as file:
                    temporary = Path(file.name)
                    file.write(label + "\n")
                os.replace(temporary, self.path)
            except OSError:
                pass
            finally:
                if temporary is not None:
                    with contextlib.suppress(OSError):
                        temporary.unlink()
        started = time.monotonic()
        outcome = "failed"
        try:
            yield
            outcome = "ready"
        finally:
            if self.enabled:
                elapsed = time.monotonic() - started
                print(
                    f"proveo-seed: {label} ({elapsed:.3f}s, {outcome})",
                    file=sys.stderr,
                    flush=True,
                )
