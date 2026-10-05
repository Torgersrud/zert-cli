# zert-cli

Customer CLI for the zert sandbox service.

## Install

```sh
brew tap Torgersrud/zert
brew install zert
```

Or build from source:

```sh
go install .   # or ./build.sh for cross-compiled dist/ binaries
```

Releases (prebuilt binaries for linux/darwin/windows, amd64/arm64) are on the
[releases page](https://github.com/Torgersrud/zert-cli/releases); tagging
`vX.Y.Z` builds and publishes them automatically.

## Security notes

- **HTTPS required.** The API host must use `https://`. Plain `http://` is
  only accepted for loopback (`localhost`, `127.0.0.0/8`, `::1`) so local
  development works. `zert login --insecure` will send credentials over a
  cleartext `http://` connection and prints a warning — avoid it.
- **Credentials file.** The bearer token is stored at
  `~/.config/zert/credentials` (see `os.UserConfigDir`) and is written with
  `0600` permissions. Keep it private; it grants access to your account for up
  to 30 days.
- **`ZERT_TUNNEL_TOKEN` in the child environment.** `zert vm` and `zert <path>`
  spawn `ssh`/`scp` with a `ProxyCommand=zert tunnel <id>` and pass the session
  token to that child through the `ZERT_TUNNEL_TOKEN` environment variable
  (never on the command line). If your `~/.ssh/config` uses `SendEnv *` or a
  `LocalCommand`, that token could be forwarded or exposed — review those
  settings.
- **VM host keys are trusted on first use.** Connections use
  `StrictHostKeyChecking=accept-new`, so the first connection to each VM
  records its key without verification (trust-on-first-use) through the relay.
  See `docs/FOLLOWUPS.md` for planned pinning.
