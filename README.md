# Zert

Zert-cli is the client for the [zert](https://zert.no) sandbox service. Boot a vm hosted in Norway, copy your repo, work inside an airgapped development enviroment, with access to Norwigian hosted AI. Debug, build or test knowing your data is safe.

- One static Go binary.
- Your SSH key authenticates you to *your* VMs; the session token never
  appears in argv or URLs.
- Leaving the ssh session never kills the VM. Detaching (`exit`, Ctrl-C,
  a closed laptop lid, a dead network) drops the connection, and the VM
  keeps running, Detached. A Detached VM then waits out its Idle window:
  the 4 hours it gets after its last connection closes. Only `zert kill`,
  or an expired Idle window, ends a VM.

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
zert vm                         # create a VM and ssh into it (leaving never kills it)
zert attach                     # back into a running VM (same shell, id optional)
zert ./myproject                # run a zert vm with your repo
zert extract ~/report.pdf       # copy a file/dir out of your live vm
zert ls                         # list your VMs and their time left
zert kill <id>                  # terminate a VM
zert kill all                   # terminate all your VMs
zert --version
```

`zert ./myproject` starts a VM if none is live and drops you into an ssh
session afterwards; when you leave, that VM keeps running. If one is
already live it just copies into it.

`zert attach [id]` is how you come back to a VM that is still running: with
exactly one live VM the id is optional, with several you have to name one
(`zert ls` lists them), and with none it points you at `zert vm`. An attach
to a VM whose Idle window has run out says so instead of hanging:
"vm <id> expired — start a new one with `zert vm`". Leaving an attached
session never kills the VM either — only `zert kill` or an expired Idle
window ends it.

Every interactive entry (`zert vm`, `zert attach`, and the entry after a
copy) runs inside one tmux session in the VM, named `zert`. That is what
makes a Detach restorable: your scrollback, running programs and background
jobs keep running, and the next attach lands back in the same shell. Two
attaches show the same screen (they mirror the session). To leave on purpose
without exiting your shell, use tmux's detach key `Ctrl-b d`. A VM from the
old, pre-tmux Template falls back to a plain shell: you still get in, only
without the session restore. Passing your own remote command to ssh runs it
as given, outside the session. The longest single connection is 8 hours, and
reaching that cap detaches you — it does not kill the VM.

`zert extract <vm-path> [dir]` is the way back out: it copies a file or
directory from your live VM into `[dir]` (default: the current directory).
VM paths resolve against the VM's home directory, just like after a fresh
ssh login. The entry is streamed back as one compressed tar, and extraction
refuses to write anything outside the chosen destination. It never creates
or kills a VM — start one with `zert vm` or `zert <path>` first.

To add args for the zert ssh session, pass them after the command
(e.g. `zert vm -L 8080:localhost:8080` or `zert ./myproject -L 8080:localhost:8080`).
With `zert <path>`, extra ssh args also make you enter an already-running
VM after the copy. Entering — and leaving — never ends a VM either way;
`zert kill` (or an expired Idle window) is the only way one dies. The old
`--keep` flag is still accepted on every entry command (`zert vm`,
`zert attach`, `zert <path>`) for existing scripts; it prints
`--keep is obsolete — vms no longer die on exit` and changes nothing.

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
