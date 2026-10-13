# BasicRoute

Minimal AI proxy with a single OpenAI-compatible endpoint (`/v1/*`) multi-key fallback, ordered proxy pools with failover, and reload without restart. Built for my own use case as a replacement for 9router or OmniRoute.

> **Warning:** this project is still in very early development. Things may break, configs may change, and features may be missing.

## Preview

| Home menu | Provider cards | Proxy table |
| --------- | -------------- | ----------- |
| <img src="preview/home.png" width="300" alt="Home menu"> | <img src="preview/provider.png" width="300" alt="Provider cards"> | <img src="preview/proxy.png" width="300" alt="Proxy table"> |

## Running

```bash
# Run directly from the source (for development)
go run .

# Build production binary named "basicroute"
go build -o basicroute .
```

| Flag      | Default        | Description                                                       |
| --------- | -------------- | ----------------------------------------------------------------- |
| `-config` | `config.jsonc` | path to the JSONC config file                                     |
| `-env`    | _(auto)_       | path to an explicit `.env` file; `-env -` = disable `.env` lookup |
| `-v`      | `false`        | verbose debug                                                     |

Listens on `system.host:system.port` (default `0.0.0.0:21172`).

## Configuration

Copy `config.example.jsonc` to `config.jsonc` and edit it. The file is JSONC: `//` and `/* */` comments plus trailing commas are allowed. Unknown fields are rejected, and validation errors name the exact field (e.g. `providers["acme-eu"].keys: …`).

Top-level keys: `system`, `providers`, `proxyPools`.

### `system`

| Field              | Type   | Required | Rules                                                                                                |
| ------------------ | ------ | -------- | ---------------------------------------------------------------------------------------------------- |
| `host`             | string | yes      | bind address, e.g. `0.0.0.0`                                                                         |
| `port`             | int    | yes      | 1–65535                                                                                              |
| `requireAuth`      | bool   | yes      | when `true`, at least one enabled system key is required                                             |
| `keys`             | map    | yes      | system API keys: `{ "name": { "key", "enabled" } }`; may be empty only when `requireAuth` is `false` |
| `limits.maxBodyMB` | number | yes      | must be > 0                                                                                          |
| `usageFile`        | string | no       | token-usage store path (default `/app/data/usage.json`); accumulated in memory, flushed every 3 min  |
| `proxyTestUrl`     | string | no       | URL requested when testing a proxy pool (default `https://www.gstatic.com/generate_204`)             |

### `providers`

At least one provider is required. The map key is the internal slug; the public model namespace is `prefix`.

| Field         | Type   | Required | Rules                                                                                                                                   |
| ------------- | ------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `name`        | string | yes      | display name                                                                                                                            |
| `prefix`      | string | yes      | unique, `^[a-z0-9-]+$`; served model IDs are `prefix/model`                                                                             |
| `type`        | string | yes      | must be `"openai"` (v1)                                                                                                                 |
| `interface`   | string | yes      | `chat-completions` or `responses-api` (upstream path follows this)                                                                      |
| `baseUrl`     | string | yes      | `http(s)` URL; trailing `/` is trimmed automatically                                                                                    |
| `proxy`       | string | no       | `""` (direct) or a `proxyPools` name; also `"auto"` (try all enabled pools in `order`, failover) or `"round"` (same, rotating start = round robin) |
| `enabled`     | bool   | yes      | a disabled provider serves nothing; an enabled one needs ≥1 enabled key and ≥1 enabled model                                            |
| `timeoutSecs` | int    | yes      | must be > 0                                                                                                                             |
| `keys`        | map    | yes      | ≥1 entry: `{ "name": { "key", "order", "enabled", "proxy?" } }`; `order` ≥ 1 and unique per provider — enabled keys fail over in `order` sequence. `proxy` is optional (see [Per-key proxy override](#per-key-proxy-override)) |
| `models`      | map    | yes      | ≥1 entry (see below)                                                                                                                    |

Model entry: `{ "name", "enabled", "limit": { "context", "output" }, "created?" }`. `limit.context` and `limit.output` must be > 0 (internal routing info, not exposed via `/v1/models`). `created` is an optional unix timestamp for the `created` field in `/v1/models` (`0` / absent = omitted). Only enabled models of enabled providers are served, and every `prefix/model` ID must be globally unique.

### `proxyPools`

May be empty (`{}`) — then every provider connects direct.

| Field     | Type   | Required | Rules                                                                                                                             |
| --------- | ------ | -------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `type`    | string | no       | `"http"` (default) = standard forward proxy, `"relay"` = 9router-style http relay                                          |
| `url`     | string | yes      | `http`/`https` URL (`socks5` is not supported); `http` type may embed `user:password@` (masked in UI and logs); `relay` takes no credentials |
| `order`   | int    | yes      | ≥ 1 and unique across pools — the sequence used by the `auto` / `round` proxy modes                                                |
| `enabled` | bool   | yes      | pool flag, shown in the TUI card indicator                                                                                        |

A pool may not be named `auto` or `round` (case-insensitive): those are reserved proxy-mode keywords, and a config that uses them is rejected at load/reload with `proxyPools["auto"]: reserved name …`. `order` is mandatory and must be unique, and a mode (`auto`/`round`) requires at least one **enabled** pool — otherwise loading fails (`… .proxy: mode "auto" needs >=1 enabled proxy pool`) instead of silently serving nothing.

**Proxy modes.** A provider's `proxy` field picks one of four behaviors:

- `""` — direct connection.
- a pool name — that pool only; if the pool is disabled the provider errors out.
- `"auto"` — try every **enabled** pool, in `order` sequence, moving to the next pool when the current one is confirmed dead (see failover below).
- `"round"` — same candidate set, but the starting point rotates per request (round robin), so load spreads across pools.

Two proxy flavors:

- **`http`** — standard forward proxy (`711Proxy`, `9Proxy`, etc): `http://customer123:your_password@proxy.example.com:8080`. Credentials travel as `Proxy-Authorization`, the TLS session stays end-to-end. Percent-encode the password if it contains `@ : / ? # %` (e.g. `p@ss:wo/rd` → `p%40ss%3Awo%2Frd`).
- **`relay`** — self-hosted http relay on free serverless hosting (Vercel / Cloudflare Worker / Deno, using the 9router template): `https://your-relay.vercel.app`. The request is sent to the relay URL with `x-relay-target: https://api.provider.com` and `x-relay-path: /v1/...` headers; the relay `fetch()`es upstream from the edge IP. No auto-deploy here — deploy the relay yourself, then point `url` at it.

#### Per-key proxy override

A provider key may pin itself to a proxy, overriding the provider's `proxy` for requests that use that key:

| `keys[].proxy` value | Behavior                                                     |
| -------------------- | ------------------------------------------------------------ |
| _(absent)_           | inherit the provider's `proxy` (direct / pool / auto / round) |
| `""`                 | direct connection, ignoring the provider's proxy             |
| `"pool-name"`        | that single pool only                                        |

Only `""` (direct) or a pool name are allowed for keys — the `auto` / `round` mode keywords are **rejected** at load (`providers[…].keys[…].proxy: proxy modes are not supported for keys`), and an unknown pool name is rejected too.

A pinned key is **not** load-balanced and has **no fallback**: if the pool itself fails (transport error or `407`, confirmed after 3 consecutive attempts like any pool), the pool is flagged red and the whole request fails with `502` — it does not fall back to the provider's pools nor to the next key. Upstream statuses (`429`/`5xx`) keep the normal key-failover behavior.

### Flexible numbers & booleans

Numeric and boolean fields accept native JSON values **or** quoted strings: `"port": 21172` and `"port": "<ENV:PORT|21172>"` are both fine, as are `"enabled": "true"` / `"1"` and `"maxBodyMB": "10"`.

### `<ENV:...>` placeholders

Any value can come from the environment:

| Form                  | Meaning                                            |
| --------------------- | -------------------------------------------------- |
| `<ENV:NAME>`          | required — startup/reload fails if `NAME` is unset |
| `<ENV:NAME\|default>` | fallback value when `NAME` is unset                |
| `<ENV:NAME:-default>` | same as above (both spellings accepted)            |

- Inside a quoted string they expand as substrings and are JSON-escaped automatically: `"baseUrl": "https://<ENV:DOMAIN>/v1"`.
- Unquoted, the resolved value must be a valid JSON fragment: `"port": <ENV:PORT>`.
- Placeholder names must match `[A-Za-z_][A-Za-z0-9_]*` and cannot span lines.
- Lookup order: OS environment variables always win, then `.env` files (`./.env` overrides `<config-dir>/.env`).
- `.env` discovery: `<config-dir>/.env`, then `./.env`. `-env <path>` uses one explicit file instead; `-env -` disables `.env` lookup. A missing explicit file is an error; missing auto-discovered files are silently skipped.
- `.env` format: `KEY=value`, `#` full-line comments, `export KEY=value` allowed, single/double-quoted values, and `VALUE # inline comment` (space before `#`).

## Endpoints

| Method | Path                   | Auth | Description                                   |
| ------ | ---------------------- | ---- | --------------------------------------------- |
| GET    | `/health`              | no   | `{ok, generation, inFlight, draining, stats}` |
| GET    | `/v1/models`           | yes  | list models (`id` = `prefix/model`)           |
| GET    | `/v1/models/{id}`      | yes  | single model                                  |
| POST   | `/v1/chat/completions` | yes  | proxy failover + key fallback, SSE when `stream:true`  |
| POST   | `/v1/responses`        | yes  | same, upstream path follows provider config   |

Auth: `Authorization: Bearer <system key>` when `requireAuth: true`. Model IDs are `prefix/model`; error responses follow the OpenAI `{message, type, param, code}` envelope.

## Reload

Press `r` in the TUI, type `r` + Enter in pipe mode, or send SIGHUP (Linux). Invalid configs are rejected, the running one keeps serving.

## TUI (read-only)

When stdin is an interactive terminal, BasicRoute opens a fullscreen TUI (gray/black/white, no CRUD — it never mutates config). Keys and proxy passwords are always masked (`****` + last 4 chars). Screenshots: see [Preview](#preview).

| Key              | Action                                                             |
| ---------------- | ------------------------------------------------------------------ |
| `↑`/`↓`/`←`/`→`  | move menu / card selection (grid wraps around)                     |
| `Enter`          | open page / provider / proxy detail                                |
| `Esc`            | back                                                               |
| `x` (Api Key)    | reveal / mask all keys                                             |
| `/` then command | same commands as pipe mode (`provider acme`, `models`, `stats`, …) |
| `t` (Proxy)      | test the focused proxy pool (yellow = testing, green = ok, red = failed; memory only) |
| `y` (Proxy)      | test all selected pools sequentially (`shift+↑↓` / `ctrl+a` to select) |
| `t` (Provider detail) | test the focused row, or all selected rows (bulk, sequential; keys and models are separate sections and cannot be mixed) — key: `GET /models`; model: minimal chat using the first valid key |
| `shift+↑↓` (table) | extend selection from the anchor, Windows Explorer style (reversing direction unselects; in provider detail it never crosses the Keys/Models sections) |
| `ctrl+↑↓` (table) | jump-collapse to the first / last row (provider detail: first / last row of the active section) |
| `ctrl+shift+↑↓`, `shift+Home/End` (table) | extend selection to the edge |
| `ctrl+a` (table) | select all (provider detail: active section only) |
| `space` (table) | collapse to the focused row only (`Enter` still opens details) |
| `r`              | reload config                                                      |
| `q` / `Ctrl+C`   | graceful shutdown                                                  |

Pages: **Api Key** (system key table: `NAMA | KEYS | TOTAL TOKENS | LAST`, green `•` = enabled, gray = disabled, `x` reveals/masks all keys, totals + last-request refresh every 3 s while the page is open, `-` = never used; Explorer-style selectable table), **Logs** (dedicated slog window, follows tail, `End` re-follows), **Provider** (responsive card grid: `•` name, inline `Keys: a/b  Models: c/d`, `Proxy:` showing `direct` / pool name / `auto (n)` / `round (n)` — green `•` = enabled, gray = disabled — → detail with _Basic info_, _Keys_, _Models_ sections, Keys table `NAME | ORDER | COOLDOWN | PROXY | KEY` where `PROXY` shows the effective per-key proxy (`direct` / pool name, dimmed when inherited from the provider), Models table, both Explorer-style selectable, `t` tests the focused row or all selected rows), **Proxy** (table: `NAME | TYPE | ORDER | CLIENTS | URL` termasking, rows sorted by `order` — yellow `•` = testing, green `•` = enabled, gray = disabled, red `•` = flagged error — `t` tests the highlighted pool, `y` tests the selected pools; a red pool is deprioritized on the request path — see failover below).

Provider health flags (memory only, `t` in detail or automatic): while a row is being tested its `•` is yellow, then turns green (ok) or red (error). A red `•` means key/model flagged error; selecting a flagged row shows the full error message at the bottom of the container. A flagged key is skipped by incoming requests while a healthy key exists (last-resort: all flagged keys are still tried) and is silently re-tested in the background (min. 60 s per provider) — recovery clears the flag. A flagged model gets exactly one retry: success clears the flag and serves the request; failure keeps the flag and returns the upstream error. A healthy model that keeps failing is auto-flagged after 3 consecutive non-2xx, non-auth, non-429 responses; any success resets the counter. Auth failures (401/403) flag the key instead of the model.

Proxy failover & flagging (memory only, applies to `auto` / `round` and to named pools): each request walks the candidate pools in order (`round` rotates the starting pool per request). A failure that points at the pool itself — a transport error (dial/refused/reset/timeout) or `407 Proxy Authentication Required` — is retried on the **same** pool up to 3 consecutive times before the pool is declared dead: it is flagged red (with the last error message, visible on the Proxy page and in `proxy <name>`) and the request moves on to the next pool. Upstream statuses such as `429`/`5xx` are **not** blamed on the pool — they keep the existing key-failover behavior inside the same pool. A flagged pool is deprioritized: it is skipped while at least one healthy candidate exists, and only used as a last resort when every candidate is flagged (anti-outage), exactly like the key rule. Any success clears the flag and resets the counter, and a flagged pool is re-tested silently in the background (min. 60 s per pool) whenever a request arrives, so a recovered pool turns green again on its own.

Bulk testing: with several rows selected in the Keys (or Models) table, `t` runs them one by one, in order, each row going yellow while it is tested. Keys and models live in separate sections, so a selection never mixes them.

Piped/non-terminal stdin falls back to the plain line CLI:

| Command                   | Description                                              |
| ------------------------- | -------------------------------------------------------- |
| `help`                    | list commands                                            |
| `status`                  | server summary (addr, gen, uptime, counts)               |
| `system`                  | system detail (host/port/auth/limits/keys)               |
| `apikey [name]`           | detail of one system key (no arg = same as system)       |
| `providers`               | table of all providers                                   |
| `provider <slug\|prefix>` | detail of one provider (keys with effective proxy + models, cooldown status) |
| `models [filter]`         | list `prefix/slug` model IDs (optional substring filter) |
| `proxies`                 | table of all proxy pools (order, type, health) + which providers use them; also lists providers on `auto`/`round` |
| `proxy <name>`            | detail of one proxy pool                                 |
| `stats`                   | hits/errors/avg latency per `provider\|model`            |
| `r` / `reload`            | reload config                                            |
| `q` / `quit`              | graceful shutdown                                        |

## Example

```bash
BASE=http://localhost:21172
KEY=br_admin_example_change-me

curl $BASE/health
curl -H "Authorization: Bearer $KEY" $BASE/v1/models

# chat (streaming: add "stream":true + -N flag)
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' \
  $BASE/v1/chat/completions
```
