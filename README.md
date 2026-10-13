# BasicRoute

Minimal AI proxy with a single OpenAI-compatible endpoint (`/v1/*`) multi-key fallback, proxy pools, and reload without restart. Built for my own use case as a replacement for 9router or OmniRoute.

> **Warning:** this project is still in very early development. Things may break, configs may change, and features may be missing.

## Preview

| Home menu | Provider cards | Proxy cards |
| --------- | -------------- | ----------- |
| <img src="preview/home.png" width="300" alt="Home menu"> | <img src="preview/provider.png" width="300" alt="Provider cards"> | <img src="preview/proxy.png" width="300" alt="Proxy cards"> |

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

### `providers`

At least one provider is required. The map key is the internal slug; the public model namespace is `prefix`.

| Field         | Type   | Required | Rules                                                                                                                                   |
| ------------- | ------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `name`        | string | yes      | display name                                                                                                                            |
| `prefix`      | string | yes      | unique, `^[a-z0-9-]+$`; served model IDs are `prefix/model`                                                                             |
| `type`        | string | yes      | must be `"openai"` (v1)                                                                                                                 |
| `interface`   | string | yes      | `chat-completions` or `responses-api` (upstream path follows this)                                                                      |
| `baseUrl`     | string | yes      | `http(s)` URL; trailing `/` is trimmed automatically                                                                                    |
| `proxy`       | string | no       | name of a `proxyPools` entry; absent or empty = direct connection                                                                       |
| `enabled`     | bool   | yes      | a disabled provider serves nothing; an enabled one needs ≥1 enabled key and ≥1 enabled model                                            |
| `timeoutSecs` | int    | yes      | must be > 0                                                                                                                             |
| `keys`        | map    | yes      | ≥1 entry: `{ "name": { "key", "order", "enabled" } }`; `order` ≥ 1 and unique per provider — enabled keys fail over in `order` sequence |
| `models`      | map    | yes      | ≥1 entry (see below)                                                                                                                    |

Model entry: `{ "name", "enabled", "limit": { "context", "output" }, "created?" }`. `limit.context` and `limit.output` must be > 0 (internal routing info, not exposed via `/v1/models`). `created` is an optional unix timestamp for the `created` field in `/v1/models` (`0` / absent = omitted). Only enabled models of enabled providers are served, and every `prefix/model` ID must be globally unique.

### `proxyPools`

May be empty (`{}`) — then every provider connects direct.

| Field     | Type   | Required | Rules                                                                                              |
| --------- | ------ | -------- | -------------------------------------------------------------------------------------------------- |
| `url`     | string | yes      | `http`/`https` URL (`socks5` is not supported); may embed `user:password@` (masked in UI and logs) |
| `enabled` | bool   | yes      | pool flag, shown in the TUI card indicator                                                         |

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
| POST   | `/v1/chat/completions` | yes  | proxy + key fallback, SSE when `stream:true`  |
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
| `r`              | reload config                                                      |
| `q` / `Ctrl+C`   | graceful shutdown                                                  |

Pages: **Api Key** (system key list, detail visible inline — green `•` = enabled, gray = disabled — `x` reveals/masks all keys), **Logs** (dedicated slog window, follows tail, `End` re-follows), **Provider** (responsive card grid: `•` name, inline `Keys: a/b  Models: c/d`, `Proxy:` — green `•` = enabled, gray = disabled — → detail with _Basic info_, _Keys_, _Models_ sections), **Proxy** (responsive card grid: `•` name, `Clients: n` providers using it — green `•` = pool enabled, gray = disabled — → detail).

Piped/non-terminal stdin falls back to the plain line CLI:

| Command                   | Description                                              |
| ------------------------- | -------------------------------------------------------- |
| `help`                    | list commands                                            |
| `status`                  | server summary (addr, gen, uptime, counts)               |
| `system`                  | system detail (host/port/auth/limits/keys)               |
| `apikey [name]`           | detail of one system key (no arg = same as system)       |
| `providers`               | table of all providers                                   |
| `provider <slug\|prefix>` | detail of one provider (keys + models, cooldown status)  |
| `models [filter]`         | list `prefix/slug` model IDs (optional substring filter) |
| `proxies`                 | table of all proxy pools + which providers use them      |
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
