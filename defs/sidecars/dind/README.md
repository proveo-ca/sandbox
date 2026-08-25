# dind sidecar

Docker-in-Docker is a **sidecar only** — never a runnable harness target.

It runs from the public `docker:dind` image. Host orchestration is **Go only**
(`internal/dind`, driven by `proveo run` when the harness manifest has
`docker: dind`).

Enable when the project scope has a Dockerfile/Compose file and either
`PROVEO_DIND=1` or an interactive yes on a TTY.

Security: the sidecar is `--privileged` and exposes an unauthenticated Docker
socket to the agent. Only enable it for project code you trust.
