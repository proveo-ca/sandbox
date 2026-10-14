#!/usr/bin/env python3
# SPEC: _spec/internal/sbx/startup-dispatch.puml
"""Sample agent responsiveness inside a proveo sbx VM, bucketed by seconds since boot.

Usage, as the first input of a fresh `proveo run claudecode` session:
    ! python3 scripts/proveo-jitter-probe.py 90 &
Writes /tmp/proveo-jitter-<boot>.txt and prints the same table when done.
"""
import os
import socket
import sys
import time

DUR = float(sys.argv[1]) if len(sys.argv) > 1 else 90.0
TICK = 0.05
HZ = os.sysconf("SC_CLK_TCK")
BOOT = int(next(l.split()[1] for l in open("/proc/stat") if l.startswith("btime")))


def cmdline(pid):
    try:
        return open(f"/proc/{pid}/cmdline", "rb").read().replace(b"\0", b" ").decode(errors="replace")
    except OSError:
        return ""


def cpu_ms(path):
    try:
        f = open(path).read().rsplit(")", 1)[1].split()
        return (int(f[11]) + int(f[12])) * 1000 / HZ
    except (OSError, IndexError, ValueError):
        return None


def agents():
    return [p for p in os.listdir("/proc") if p.isdigit() and "npm-global/bin/claude" in cmdline(p)]


def procs():
    out = {}
    for p in os.listdir("/proc"):
        if p.isdigit():
            c = cpu_ms(f"/proc/{p}/stat")
            if c is not None:
                out[p] = c
    return out


try:
    GW = socket.gethostbyname("gateway.docker.internal")
except OSError:
    GW = None


def connect_ms():
    if GW is None:
        return None
    t = time.perf_counter()
    try:
        socket.create_connection((GW, 3128), timeout=3).close()
    except OSError:
        return None
    return (time.perf_counter() - t) * 1000


buckets = {}
heavy = {}
last_main = {}
last_procs, last_procs_at = procs(), time.monotonic()
end, i = time.monotonic() + DUR, 0
while time.monotonic() < end:
    a = time.perf_counter()
    time.sleep(TICK)
    over = (time.perf_counter() - a - TICK) * 1000
    b = buckets.setdefault(int(time.time() - BOOT) // 5 * 5, {"n": 0, "busy": 0, "run": 0, "best": 0, "over": [], "conn": []})
    b["n"] += 1
    b["over"].append(over)
    busy = False
    for p in agents():
        c = cpu_ms(f"/proc/{p}/task/{p}/stat")
        if c is not None and p in last_main and c - last_main[p] >= TICK * 1000 * 0.8:
            busy = True
        last_main[p] = c
    b["busy"] += busy
    b["run"] = b["run"] + 1 if busy else 0
    b["best"] = max(b["best"], b["run"])
    if i % 5 == 0:
        c = connect_ms()
        if c is not None:
            b["conn"].append(c)
    if time.monotonic() - last_procs_at >= 1:
        now, now_at = procs(), time.monotonic()
        span = (now_at - last_procs_at) * 1000
        for p, c in now.items():
            pct = (c - last_procs.get(p, c)) * 100 / span
            if pct >= 25:
                name = os.path.basename(cmdline(p).split(" ", 1)[0]) or p
                heavy.setdefault(int(time.time() - BOOT) // 5 * 5, {}).setdefault(name, 0)
                heavy[int(time.time() - BOOT) // 5 * 5][name] = max(heavy[int(time.time() - BOOT) // 5 * 5][name], round(pct))
        last_procs, last_procs_at = now, now_at
    i += 1


def q(v, p):
    v = sorted(v)
    return v[min(len(v) - 1, int(len(v) * p))] if v else float("nan")


lines = [f"boot={BOOT}  agents={','.join(agents())}  host_gateway={GW}",
         "since_boot | agent_main_busy(50ms ticks, longest run) | wake_late p99/max | host_connect p50/max | heavy procs (>=25% cpu)"]
for k in sorted(buckets):
    b = buckets[k]
    lines.append(f"+{k:3d}..{k+5:3d}s | busy {b['busy']:3d}/{b['n']:3d} longest {b['best']*50:5d}ms | "
                 f"late {q(b['over'], .99):6.1f}/{max(b['over']):6.1f}ms | "
                 f"connect {q(b['conn'], .5):6.1f}/{max(b['conn']) if b['conn'] else float('nan'):6.1f}ms | "
                 + ", ".join(f"{n} {v}%" for n, v in sorted(heavy.get(k, {}).items(), key=lambda x: -x[1])[:4]))
report = "\n".join(lines) + "\n"
path = f"/tmp/proveo-jitter-{BOOT}.txt"
open(path, "w").write(report)
print(report + f"written to {path}")
