# Zert

Zert-cli is the client for the [zert](https://zert.no) sandbox service. Boot a vm hosted in Norway, copy your repo, work inside an airgapped development enviroment, with access to Norwigian hosted AI. Debug, build or test knowing your data is safe.

- One static Go binary.
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
zert login                      
zert vm                         # creates a VM, and instantly ssh into it 
zert ./myproject                # run a zert vm with your repo
zert ls                         # list your VMs
zert kill <id>                  # terminate a VM
zert --version
```

`zert ./myproject` starts a VM if none is live and drops you into an ssh
session afterwards (killing that VM on exit); if one is already live it just
copies into it.

To add args for zert ssh tunnel you can pass the arguments after like this
(e.g. `zert vm -L 8080:localhost:8080`).

## Configuration

- Credentials live in `~/.config/zert/credentials`
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
## License

MIT — see [LICENSE](LICENSE).
