# mitmproxy Inspector Harness

Headless [mitmproxy](https://mitmproxy.org/) (`mitmdump`) is the first-hop
inspection proxy for agent egress. In `firewall` mode it forwards all
HTTP(S) traffic to Squid as its enforcement upstream so that mitmproxy records
the attempt and Squid remains the policy/egress boundary:

```txt
agent -> mitmproxy (decrypt + record) -> squid (enforce) -> internet
```

mitmproxy replaced the previous Charles inspector. The reasons:

- **No license.** mitmproxy is Apache-2.0; Charles required a commercial seat,
  which was the only reason it had been chosen.
- **Headless-native.** Configuration is CLI flags / addons. There is no
  GUI-generated config blob to seed, mount, or keep from being overwritten.
- **Real HTTPS inspection.** mitmproxy terminates TLS and records the decrypted
  method, path, and host. The old Charles wiring never bumped TLS, so it only
  ever saw `CONNECT host:443`. This is the upgrade implied by the word
  "firewall".

## HTTPS Interception and Trust

HTTPS interception is **on**. mitmproxy generates a CA on first start and writes
it to its confdir (`/mitmproxy-confdir/mitmproxy-ca-cert.pem`). For the agent's
TLS to succeed, it must trust that CA.

Because the egress topology forces **all** agent traffic through mitmproxy,
every certificate the agent sees is signed by the mitmproxy CA. The egress
lifecycle therefore mounts the generated CA cert into the agent and points the
standard CA environment variables at it:

```txt
SSL_CERT_FILE / REQUESTS_CA_BUNDLE / NODE_EXTRA_CA_CERTS / CURL_CA_BUNDLE / GIT_SSL_CAINFO
```

No host CA store surgery is needed, and provider TLS is unaffected — mitmproxy
itself validates the real origin certificates upstream (via Squid).

## Upstream Enforcement

mitmproxy is started with `--mode upstream:http://squid:3128`. The chain is
fail-closed by construction: mitmproxy is attached only to internal Docker
networks and has no internet route of its own, so if the upstream is missing it
simply cannot egress. There is no config file that can silently drift.

## Flow Export

The bundled addon `addons/ndjson_dump.py` appends one JSON record per flow to
`/flows/flows.ndjson` with the fields the egress dashboard normalizes (`ts`,
`source`, `decision`, `protocol`, `method`, `host`, `port`, `path`, `status`,
`reason`). The egress lifecycle persists these under
`$PROVEO_EGRESS_ROOT/egress/<session-id>/mitmproxy/flows/` — a host-side state
dir kept OUTSIDE the agent's bind mounts (default `~/.local/state/proveo`) so the
sandboxed agent cannot read the mitmproxy CA key or tamper with its own flow log.

## Standalone Use

```bash
mkdir -p flows confdir
# Direct proxy for local inspection (set PROVEO_MITM_UPSTREAM to chain, e.g. http://squid:3128)
docker run -it --rm -p 8888:8888 \
  -e PROVEO_MITM_PORT=8888 -e PROVEO_MITM_UPSTREAM= \
  -v "$PWD/flows:/flows" -v "$PWD/confdir:/mitmproxy-confdir" \
  proveo/mitmproxy:local
# Debug shell: the same with --entrypoint /bin/bash
```

## Network Invariant

In `firewall` mode:

- the agent can only reach `mitm:8888`,
- `mitm` can only reach `squid:3128`,
- only `squid` has internet-capable egress.

Proxy env vars are not the boundary; Docker network attachment is.
