# Changelog

## v0.8.0

A security release. Commands, file operations and chat are now end-to-end encrypted, password keys are derived with PBKDF2, and the relay is hardened against slow or hostile clients.

### ⚠️ Breaking changes

- **Protocol 0.8 is not compatible with v0.7.x.** Hosts, relays and clients must all run v0.8.0. The browser UI is served by the relay, so a relay must be upgraded together with the hosts that use it.
- **Relay operators** (including the public relay `my.rmte.biz.id`): upgrade the relay at the same time as you publish or install the new release, otherwise v0.8.0 hosts cannot log in.
- **`--max-buffer=0` now means 256 MB** instead of unlimited, and also bounds the WebSocket frame size.
- **Web Preview only forwards ports that were opened explicitly.** The Web UI opens a port automatically when you preview it; use `--preview-ports` to fix the allowed list.
- **Client IPs from proxy headers** (`CF-Connecting-IP`, `X-Forwarded-For`, `X-Real-IP`) are only trusted from `--trusted-proxies` (default `loopback`). A relay behind a proxy on another machine must list that proxy.

### Upgrade

1. Upgrade the relay (`rmte update --restart` for a background relay, or replace the binary and restart).
2. Upgrade every host: `rmte update --restart`. Restarting with the same `--id` and password resumes the session.
3. Reload open browser tabs and upgrade CLI / MCP clients (`rmte update`).

### Security

- All viewer↔host control messages (file operations, `ai_exec` and its output, tabs, presence, chat) travel in an encrypted, authenticated envelope with sequence numbers. A relay can no longer read or forge commands, delete files or replay messages.
- Terminal and file frames authenticate their header (direction, tab, transfer id), so frames cannot be redirected to another tab or reflected back.
- Keys are derived with PBKDF2-HMAC-SHA256 (600,000 iterations, salted with the session id) and split with HKDF into encryption, relay-auth and preview subkeys. The relay never sees the password, and every password guess costs a full PBKDF2 run. Nothing is stored on disk.
- Relay writes are queued per connection with a write deadline, so a client that stops reading can no longer stall the relay. Frames are limited to 64 KB before authentication.
- Web Preview cookies expire after 30 minutes (renewed while in use) and only explicitly opened ports are reachable.
- Admin dashboard: login rate limiting with backoff, `Secure` cookie over HTTPS.
- Browsers may only open relay WebSockets from the relay's own origin or `--ws-origins`.
- IP bans and login throttling can no longer be bypassed with forged proxy headers.
- DNS only falls back to public resolvers when no resolver is configured (`RMTE_DNS_FALLBACK=off` disables it).
- Releases are built only after tests pass, with GitHub Actions pinned to commit SHAs and signed build provenance (`gh attestation verify`).

### Fixes

- `rmte serve --sandbox` now actually enables the sandbox (the flag was not passed to the embedded host).
- Long `ai_exec` commands no longer block keystrokes and file operations of other viewers.
- Terminal tab ids no longer wrap around onto existing tabs after 254 tabs.
- Chat is no longer described as encrypted when it was not; it now is.

### New flags

- `--preview-ports` (`share`, `serve`): fixed Web Preview allowlist.
- `--trusted-proxies` (`serve`): proxies whose client-IP headers are trusted.
- `--ws-origins` (`serve`): extra browser origins allowed to connect.

### Docs

- README and SECURITY.md describe exactly what is and is not end-to-end encrypted, including that every password holder is fully trusted.
- The flag reference in README and on rmte.biz.id is now identical and complete.
