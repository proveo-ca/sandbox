# SPEC: _spec/defs/opencode/native-v2-integration.puml

import json
import os
from pathlib import Path
import signal
import sqlite3
import sys
import time

from test_opencode_credentials import FAKE_VALUE, SCHEMA


def event(name, **values):
    with open(os.environ["PROVEO_RUNTIME_EVENTS"], "a") as output:
        output.write(json.dumps(dict(event=name, **values)) + "\n")


args = sys.argv[1:]
if "--version" in args or "--help" in args:
    event("diagnostic")
    sys.exit(0)
dbfile = Path(os.environ["OPENCODE_DB"])
if not dbfile.exists():
    db = sqlite3.connect(dbfile)
    db.executescript(SCHEMA)
else:
    db = sqlite3.connect(dbfile)
assert db.execute("SELECT count(*) FROM credential").fetchone() == (0,)
assert os.environ["OPENAI_API_KEY"] == "SYNTHETIC_ENV_KEY"
db.execute("PRAGMA journal_mode = WAL")
db.execute(
    "INSERT INTO credential VALUES ('during', 'fixture', 'during', ?, NULL, NULL, 1, 1, 2)",
    (FAKE_VALUE,),
)
db.commit()
event("ready", database=str(dbfile), arguments=args, pid=os.getpid())
mode = args[0]
if mode == "debug":
    db.close()
    sys.exit(0)


def append():
    seq = db.execute("SELECT max(seq) FROM session_message").fetchone()[0] + 1
    db.execute(
        "INSERT INTO session_message VALUES (?, 'session-fixture', ?, 'keep new message')",
        ("message-" + str(seq), seq),
    )
    db.commit()


def finish(sig, _):
    append()
    db.close()
    event("stopped")
    sys.exit(128 + sig)


if mode == "hold":
    signal.signal(signal.SIGTERM, finish)
    signal.signal(signal.SIGINT, finish)
    signal.signal(signal.SIGHUP, finish)
    while True:
        time.sleep(0.05)
elif mode == "detach":
    pid = os.fork()
    if pid == 0:
        os.setsid()

        def detached_finish(*_):
            time.sleep(0.6)
            append()
            db.close()
            event("detached-stopped")
            os._exit(0)

        signal.signal(signal.SIGTERM, detached_finish)
        event("detached")
        while True:
            time.sleep(0.05)
    time.sleep(0.2)
    os._exit(0)
elif mode == "damage":
    append()
    db.execute("ALTER TABLE credential ADD COLUMN unsupported_secret text")
    db.commit()
elif mode == "obstruct":
    append()
    Path(os.environ["PROVEO_OPENCODE_DURABLE_DATA"], "opencode.db-wal").write_bytes(
        b"synthetic publication obstruction"
    )
elif mode == "preferences":
    state = Path(os.environ["XDG_STATE_HOME"], "opencode")
    state.mkdir(parents=True, exist_ok=True)
    (state / "prompt-history.jsonl").write_text(
        '{"text":"first","pasted":[]}\n{"text":"second","pasted":[]}\n'
    )
    (state / "prompt-stash.jsonl").write_text(
        '{"prompt":{"text":"stashed","pasted":[]},"timestamp":2}\n'
    )
    (state / "model.json").write_text(
        '{"recent":["second","first"],"favorite":["first"]}'
    )
    (state / "session.json").write_text('{"pinned":["session-fixture"]}')
    (state / "service.json").write_text(
        '{"password":"SYNTHETIC_TRANSIENT_SERVER_SECRET"}'
    )
    append()
elif mode == "final-disk-full":
    append()
    dbfile.with_name("latest-native-write").write_text("latest session was committed")
else:
    append()
db.close()
