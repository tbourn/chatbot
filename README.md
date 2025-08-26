# ⚡ go-chat-backend

> Production-minded Go backend for chat apps — clean layers, SQLite (pure Go), retrieval-augmented answers, **idempotency**, **rate limiting**, and first-class **observability** (OTel + Prometheus). Secure defaults, Swagger-ready, and easy to run with Docker. 🚀

---

## 📚 Table of Contents

- [⚡ go-chat-backend](#-go-chat-backend)
  - [📚 Table of Contents](#-table-of-contents)
  - [🧾 Description](#-description)
  - [✅ Features](#-features)
  - [🛡️ Security \& Compliance](#️-security--compliance)
  - [🔧 Requirements](#-requirements)
  - [⚙️ Installation](#️-installation)
    - [macOS / Linux](#macos--linux)
    - [Windows](#windows)
  - [⚡ Environment Configuration](#-environment-configuration)
  - [▶️ Running](#️-running)
    - [Run locally (Go)](#run-locally-go)
    - [Run with Makefile](#run-with-makefile)
      - [(Optional) fetch deps and tidy](#optional-fetch-deps-and-tidy)
      - [Build the service binary](#build-the-service-binary)
      - [Run the service (uses your .env)](#run-the-service-uses-your-env)
      - [Unit tests + coverage summary](#unit-tests--coverage-summary)
      - [Open HTML coverage report (generates coverage.html)](#open-html-coverage-report-generates-coveragehtml)
      - [Build Docker image (tag: go-chat-backend:local)](#build-docker-image-tag-go-chat-backendlocal)
      - [Bring up via docker compose (uses your docker-compose.yml)](#bring-up-via-docker-compose-uses-your-docker-composeyml)
      - [Tear down compose stack and volumes](#tear-down-compose-stack-and-volumes)
    - [Run with Docker](#run-with-docker)
      - [Build the image](#build-the-image)
      - [Run the container (reads env from .env)](#run-the-container-reads-env-from-env)
      - [Build and start (uses your .env and compose file)](#build-and-start-uses-your-env-and-compose-file)
      - [Stop \& clean up](#stop--clean-up)
      - [Health/metrics quick checks](#healthmetrics-quick-checks)
  - [🔌 Observability](#-observability)
  - [🌐 API Overview](#-api-overview)
    - [Headers \& Auth](#headers--auth)
    - [Idempotency](#idempotency)
    - [Error Envelope](#error-envelope)
  - [📖 Full Endpoint Documentation](#-full-endpoint-documentation)
    - [🗂️ Chats](#️-chats)
      - [Create Chat](#create-chat)
      - [List Chats (paginated, ETag)](#list-chats-paginated-etag)
      - [Update Chat Title](#update-chat-title)
    - [💬 Messages](#-messages)
      - [Post Message (answer + store) — *idempotent*](#post-message-answer--store--idempotent)
      - [List Messages (paginated, ETag)](#list-messages-paginated-etag)
    - [👍 Feedback](#-feedback)
      - [Leave Feedback on a Message](#leave-feedback-on-a-message)
    - [🩺 Admin \& Ops](#-admin--ops)
      - [Health](#health)
      - [Metrics (Prometheus)](#metrics-prometheus)
      - [Swagger UI *(if enabled in main)*](#swagger-ui-if-enabled-in-main)
  - [🧪 Testing](#-testing)
  - [👨‍💻 Author \& Maintainer](#-author--maintainer)

---

## 🧾 Description

**go-chat-backend** is a compact, production-ready HTTP service that powers chat UIs. It keeps concerns clean:

- HTTP (Gin) → **services** (rules) → **repo** (GORM) → **domain** (models)
- Pure-Go SQLite driver (no CGO), safe in containers
- Retrieval-augmented answers from Markdown data
- Transport-level **idempotency** and per-user/IP **rate limiting**
- **OpenTelemetry** tracing + **Prometheus** metrics + structured logs with PII redaction

Perfect for prototypes that must behave like real services — and for learning modern Go service design.

---

## ✅ Features

- 🧱 **Clean layering:** handlers → services → repo → domain  
- 🗄️ **SQLite (pure Go):** FK constraints + cascades, WAL pragmas  
- 🔎 **Retrieval index:** deterministic, concurrency-safe in-memory search  
- 🔁 **Idempotency:** `Idempotency-Key` replays without burning rate tokens  
- 🚦 **Rate limiting:** per-user/IP token bucket (opportunistic GC)  
- 🧭 **Observability:** OTLP traces, Prometheus `/metrics`, request-scoped logs  
- 🧼 **Security posture:** Request IDs, panic recovery, CORS, optional HSTS, PII-redacting logger  
- 📜 **Swagger-ready:** turn on with `SWAGGER_ENABLED=true`  

---

## 🛡️ Security & Compliance

- 🔒 Redacts `Authorization`, `Cookie`, `Set-Cookie` and custom sensitive headers
- ✂️ Scrubs emails/phones/UUIDs in query strings & headers
- ✅ Strict, stable error envelope with request correlation
- ♻️ Idempotency prevents duplicate side effects
- 🚧 Rate limiting to dampen abuse & cost
- 🌐 CORS posture: allow-all (no credentials) by default or lock down via env
- ⚠️ **You own production hardening:** authn/z, secrets, TLS, backups, PII policies

---

## 🔧 Requirements

- **Go** 1.23+
- **Make** (optional)
- **Docker** (optional)
- **OTel Collector & Prometheus** (optional, for observability)

---

## ⚙️ Installation

### macOS / Linux

```bash
git clone https://github.com/tbourn/go-chat-backend.git
cd go-chat-backend

# Create .env (use the block below)
printf "" > .env  # then paste values

go mod download
go build -o bin/go-chat-backend ./cmd/server
```

### Windows

```powershell
git clone https://github.com/tbourn/go-chat-backend.git
cd go-chat-backend

ni .env           # then paste values into .env

go mod download
go build -o bin\go-chat-backend.exe .\cmd\server
```

> 💡 The app itself does **not** require CGO.

---

## ⚡ Environment Configuration

Create `.env` at repo root:

```env
PORT=8080
DB_PATH=./app.db

# Use both, since main prefers DATA_MD, and your config may read DATA_PATH
DATA_MD=./data/data.md
DATA_PATH=./data/data.md

# Retrieval threshold (fallback in main is 0.10 if unset)
THRESHOLD=0.30

OTEL_ENABLED=true
OTEL_EXPORTER_OTLP_ENDPOINT=otel:4317
OTEL_EXPORTER_OTLP_INSECURE=true
OTEL_SERVICE_NAME=go-chat-backend
OTEL_TRACES_SAMPLER_ARG=1.0

SWAGGER_ENABLED=true
DEBUG_INDEX_PROBE=1
LOG_LEVEL=debug
LOG_PRETTY=1

RATE_BURST=10
RATE_RPS=5

GIN_MODE=release

CORS_ALLOWED_ORIGINS=
ENABLE_HSTS=0

READ_TIMEOUT=15s
WRITE_TIMEOUT=20s
READ_HEADER_TIMEOUT=10s
IDLE_TIMEOUT=60s
HSTS_MAX_AGE=31536000s
IDEMPOTENCY_TTL=10s

API_BASE_PATH=/api/v1
```

---

## ▶️ Running

### Run locally (Go)

```bash
# with .env on your shell (direnv/dotenv or export manually)
go run ./cmd/server

# or, after building
./bin/go-chat-backend
```

### Run with Makefile

#### (Optional) fetch deps and tidy
```bash
make tidy
```

#### Build the service binary
```bash
make build
```

#### Run the service (uses your .env)
```bash
make run
```

#### Unit tests + coverage summary
```bash
make test
```

#### Open HTML coverage report (generates coverage.html)
```bash
make cover
```

#### Build Docker image (tag: go-chat-backend:local)
```bash
make docker-build
```

#### Bring up via docker compose (uses your docker-compose.yml)
```bash
make docker-up
```

#### Tear down compose stack and volumes
```bash
make docker-down
```


### Run with Docker

#### Build the image
```bash
docker build -t go-chat-backend:local .
```

#### Run the container (reads env from .env)
```bash
docker run --rm -p 8080:8080 --env-file .env go-chat-backend:local
```

```bash
curl -s http://localhost:8080/health
# {"status":"ok"}
```

```bash
# Example API base (adjust if API_BASE_PATH differs in .env)
curl -s http://localhost:8080/api/v1/chats
```


**docker-compose.yml:**
#### Build and start (uses your .env and compose file)
```bash
docker compose up --build
```

#### Stop & clean up
```bash
docker compose down -v
```

#### Health/metrics quick checks
```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/metrics | head
```

---

## 🔌 Observability

- **Prometheus**: scrape `/metrics` (counter/histogram/gauge for HTTP)
- **OTel tracing**: set `OTEL_ENABLED=true` and point `OTEL_EXPORTER_OTLP_ENDPOINT` to your collector (gRPC/4317).  
- **Logs**: JSON with request-scoped fields, PII redaction, and correlation via `X-Request-ID`.

---

## 🌐 API Overview

### Headers & Auth

- `X-User-ID` *(optional)* — who owns the chats. If omitted → `"demo-user"`.

### Idempotency

- `Idempotency-Key` (POST message): stable per semantic operation.  
  Replays return the prior result and add `Idempotency-Replayed: true`.

### Error Envelope

All errors return:
```json
{
  "request_id": "f95fe0d9-...",
  "code": "not_found | bad_request | forbidden | conflict | internal_error | create_failed | list_failed | answer_failed",
  "message": "human-readable text"
}
```

---

## 📖 Full Endpoint Documentation

> Default base path: **`/api/v1`** (override with `API_BASE_PATH`)

### 🗂️ Chats

#### Create Chat
**POST** `/chats`  
Create a chat for the current user.

**Headers**
- `X-User-ID` *(optional)* — owner id

**Body**
```json
{
  "title": "optional string"
}
```

**Responses**
- `201 Created` — `domain.Chat`
- `400 Bad Request` — invalid JSON
- `500 Internal Server Error` — persistence error

**cURL**
```bash
curl -sS -X POST http://localhost:8080/api/v1/chats   -H 'Content-Type: application/json'   -H 'X-User-ID: user123'   -d '{"title":"Customer insights UK"}'
```

---

#### List Chats (paginated, ETag)
**GET** `/chats`

**Headers**
- `X-User-ID` *(optional)*
- `If-None-Match` *(optional)* — weak ETag support

**Query**
- `page` *(int, default 1, min 1)*
- `page_size` *(int, default 20, min 1, max 100)*

**Responses**
- `200 OK`
```json
{
  "chats": [ /* array of domain.Chat */ ],
  "pagination": {
    "page": 1,
    "page_size": 20,
    "total": 2,
    "total_pages": 1,
    "has_next": false
  }
}
```
- `304 Not Modified` — when `If-None-Match` matches
- `500 Internal Server Error`

**Notes**
- Sets a weak `ETag: W/"chats:<user>:<count>:<max_updated_unix>"`.

---

#### Update Chat Title
**PUT** `/chats/{id}/title`

**Path**
- `id` *(UUID string)*

**Headers**
- `X-User-ID` *(optional)*

**Body**
```json
{ "title": "New name" }
```

**Responses**
- `204 No Content`
- `400 Bad Request` — invalid UUID or empty/missing title
- `404 Not Found` — chat missing or not owned
- `500 Internal Server Error`

**cURL**
```bash
curl -sS -X PUT http://localhost:8080/api/v1/chats/141add05-4415-4938-b5a1-17e0d3171aff/title   -H 'Content-Type: application/json'   -H 'X-User-ID: user123'   -d '{"title":"Research UK 18–24"}'
```

---

### 💬 Messages

#### Post Message (answer + store) — *idempotent*
**POST** `/chats/{id}/messages`

**Path**
- `id` *(UUID string, chat id)*

**Headers**
- `X-User-ID` *(optional)*
- `Idempotency-Key` *(optional, recommended)*

**Body**
```json
{
  "content": "What percentage of Gen Z in Nashville discover new brands through podcasts?"
}
```

**Responses**
- `200 OK`
```json
{
  "message": {
    "id": "uuid",
    "chat_id": "uuid",
    "role": "assistant",
    "content": "…",
    "score": 0.72,
    "created_at": "2025-08-25T09:00:00Z"
  }
}
```
- `400 Bad Request` — invalid chat id, empty content, or content too long
- `404 Not Found` — chat not found/owned
- `500 Internal Server Error` — persistence error

**Replay behavior**
- If prior success exists for `(user, chat, key)`, returns the **same assistant message**, header `Idempotency-Replayed: true`.

**cURL**
```bash
curl -sS -X POST http://localhost:8080/api/v1/chats/<chat-id>/messages   -H 'Content-Type: application/json'   -H 'X-User-ID: user123'   -H 'Idempotency-Key: 7a8d9f4c-1b2a-4c3d-8e9f-0123456789ab'   -d '{"content":"What percentage of Gen Z in Nashville discover new brands through podcasts?"}'
```

---

#### List Messages (paginated, ETag)
**GET** `/chats/{id}/messages`

**Path**
- `id` *(UUID string, chat id)*

**Query**
- `page` *(int, default 1, min 1)*
- `page_size` *(int, default 20, min 1, max 100)*

**Headers**
- `If-None-Match` *(optional)*

**Responses**
- `200 OK`
```json
{
  "messages": [
    { "id":"…", "role":"user", "content":"…", "created_at":"…" },
    { "id":"…", "role":"assistant", "content":"…", "score":0.63, "created_at":"…" }
  ],
  "pagination": {
    "page": 1,
    "page_size": 20,
    "total": 2,
    "total_pages": 1,
    "has_next": false
  }
}
```
- `304 Not Modified` — when `If-None-Match` matches
- `400 Bad Request` — invalid chat id
- `404 Not Found` — chat missing
- `500 Internal Server Error`

**Notes**
- Weak `ETag: W/"messages:<chat>:<count>:<max_updated_unix>"`.

---

### 👍 Feedback

#### Leave Feedback on a Message
**POST** `/messages/{id}/feedback`

**Path**
- `id` *(UUID string, message id)*

**Body**
```json
{ "value": 1 }   // or -1
```

**Responses**
- `204 No Content`
- `400 Bad Request` — invalid payload (`value` not `-1` or `1`)
- `403 Forbidden` — not allowed to give feedback (wrong owner or user message)
- `404 Not Found` — message not found
- `409 Conflict` — duplicate feedback for same `(message, user)`
- `500 Internal Server Error`

**cURL**
```bash
curl -sS -X POST http://localhost:8080/api/v1/messages/<message-id>/feedback   -H 'Content-Type: application/json'   -H 'X-User-ID: user123'   -d '{"value":1}'
```

---

### 🩺 Admin & Ops

#### Health
**GET** `/health`  
→ `200 {"status": "ok"}`

#### Metrics (Prometheus)
**GET** `/metrics`  
→ Prometheus text exposition with:
- `http_requests_total{method,path,status}`
- `http_request_duration_seconds_bucket{method,path,...}`
- `http_requests_inflight`
- `http_response_size_bytes_bucket{method,path,...}`

#### Swagger UI *(if enabled in main)*
- Typically served when `SWAGGER_ENABLED=true` (route depends on main wiring, e.g. `/swagger/index.html`).

---

## 🧪 Testing
```bash
go test ./... -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out -v
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

**Optional LCOV:**
```bash
GOROOT=$(go env GOROOT) go install github.com/jandelgado/gcov2lcov@latest
"$(go env GOPATH)/bin/gcov2lcov" -infile=coverage.out -outfile=coverage.lcov
```

---

## 👨‍💻 Author & Maintainer

**Thomas Bournaveas**  
📧 **[thomas.bournaveas@gmail.com](mailto:thomas.bournaveas@gmail.com)**  
🌐 **[Website](https://thomasbournaveas.com)**  
💼 **[LinkedIn](https://www.linkedin.com/in/thomas-bournaveas-35a778150/)**

---
