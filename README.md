# zert

[![CI](https://github.com/Torgersrud/zert-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/Torgersrud/zert-cli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Torgersrud/zert-cli.svg)](https://pkg.go.dev/github.com/Torgersrud/zert-cli)
[![Go Report Card](https://goreportcard.com/badge/github.com/Torgersrud/zert-cli)](https://goreportcard.com/report/github.com/Torgersrud/zert-cli)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

The `zert` CLI is the customer client for the [zert](https://zert.no) sandbox
service: boot a cloud dev VM, copy your project into it, and work inside over
plain `ssh` — `zert` wires the tunnel, keys and lifecycle for you.

- One static Go binary (linux/darwin/windows, amd64/arm64), no daemon, no deps.
- Your SSH key authenticates you to *your* VMs; the session token never
  appears in argv or URLs.
- VMs are killed automatically when you leave the shell (unless `--keep`).

## Install

```sh
brew tap Torgersrud/zert
brew install zert
```

or the Go toolchain:

```sh
go install github.com/Torgersrud/zert-cli@latest
```

Prebuilt binaries for every platform are on the
[releases page](https://github.com/Torgersrud/zert-cli/releases) (tagging
`vX.Y.Z` builds and publishes them automatically).

## Quickstart

```sh
zert login                      # email + password, token stored 0600
zert vm                         # create a VM, ssh in — Ctrl-D kills it
zert ./myproject                # copy a dir/file into your VM as ~/myproject
zert ls                         # list your VMs
zert kill <id>                  # terminate a VM
```

`zert ./myproject` starts a VM if none is live and drops you into an ssh
session afterwards (killing that VM on exit); if one is already live it just
copies into it.

## Commands

| Command | Description |
|---|---|
| `zert login [--host URL] [--insecure]` | authenticate and store credentials |
| `zert logout` | delete stored credentials (best-effort server revoke) |
| `zert me` | show profile, quota, pubkey |
| `zert ls` | list your VMs |
| `zert vm [--keep] [ssh args…]` | create a VM and ssh into it (kill on exit) |
| `zert kill <id>` | terminate a VM |
| `zert <path>` | copy a local dir/file into your VM |
| `zert version` | print the version |

Unknown extra arguments after `zert vm` are passed through to `ssh`
(e.g. `zert vm -L 8080:localhost:8080`).

## Configuration

- Credentials live in `~/.config/zert/credentials` (mode `0600`).
- Server host resolution: `--host` flag > `$ZERT_HOST` > stored credentials.
- `$ZERT_TUNNEL_TOKEN` is an internal handoff used by the hidden
  `zert tunnel` ProxyCommand child — you never set it yourself.

## Security

Short version: HTTPS is enforced (plain `http://` only for loopback or an
explicit `--insecure` flag), tokens never touch argv or URL query strings, and
server text is sanitized against terminal-escape injection. Full notes in
[SECURITY.md](SECURITY.md); known limitations and planned hardening (host-key
pinning, token revocation, idempotent create) are tracked in
[docs/FOLLOWUPS.md](docs/FOLLOWUPS.md). Report vulnerabilities privately — see
SECURITY.md.

## Development

```sh
go build ./... && go test ./...    # tests use httptest stubs, no VMs needed
./build.sh                         # static cross-compile → dist/ + SHA256SUMS
```

CI runs gofmt, `go vet`, tests, `govulncheck` and `gosec` on every push and
PR. Releases are cut by pushing a `vX.Y.Z` tag, which also bumps the Homebrew
tap formula.

## License

MIT — see [LICENSE](LICENSE).
