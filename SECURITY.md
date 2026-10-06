# Security policy

## Reporting a vulnerability

Please report security issues **privately** rather than in a public issue.

- Email: **oskar@zert.no**
- Or use [GitHub private vulnerability
  reporting](https://github.com/Torgersrud/zert-cli/security/advisories/new)
  for this repository.

Include the version (`zert version` / commit), steps to reproduce, and the
impact. We aim to acknowledge private reports within a few business days.

## Security model notes

- **HTTPS required.** The API host must use `https://`. Plain `http://` is
  only accepted for loopback (`localhost`, `127.0.0.0/8`, `::1`) so local
  development works. `zert login --insecure` will send credentials over a
  cleartext `http://` connection and prints a warning — avoid it.
- **Credentials file.** The bearer token is stored at
  `~/.config/zert/credentials` (see `os.UserConfigDir`) and is written with
  `0600` permissions. Keep it private; it grants access to your account for up
  to 30 days.
- **Token never in argv or URLs.** The hidden `zert tunnel` ProxyCommand child
  receives the session token through the `ZERT_TUNNEL_TOKEN` environment
  variable and sends it as an `authorization` header — never on the command
  line or in a WS URL query string. If your `~/.ssh/config` uses `SendEnv *`
  or a `LocalCommand`, that token could be forwarded or exposed — review those
  settings.
- **Terminal-escape sanitization.** All server-provided text printed to the
  terminal is stripped of control/escape sequences.
- **VM host keys are trusted on first use.** Connections use
  `StrictHostKeyChecking=accept-new`, so the first connection to each VM
  records its key without verification (trust-on-first-use) through the relay.
  See `docs/FOLLOWUPS.md` for planned pinning.
