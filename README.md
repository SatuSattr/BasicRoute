# BasicRoute

Lightweight AI proxy with a single OpenAI-compatible endpoint (`/v1/*`) multi-key fallback, proxy pools, and reload without restart. Built for my own use case as a replacement for 9router or OmniRoute.

> **Warning:** this project is still in very early development. Things may break, configs may change, and features may be missing.

> Don't expect a Web dashboard or CLI cs this is a minimal API-only gateway.

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

Type `r` + Enter, or send SIGHUP (Linux). Invalid configs are rejected, the running one keeps serving.

## Example

```bash
BASE=http://localhost:21172
KEY=br_admin_example_change-me

curl $BASE/health
curl -H "Authorization: Bearer $KEY" $BASE/v1/models

# chat (streaming: tambah "stream":true + flag -N)
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' \
  $BASE/v1/chat/completions
```
