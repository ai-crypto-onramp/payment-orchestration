# Payment Orchestration

Fiat ingress service that normalizes payment intents across rails and manages the full card/lifecycle from 3DS through settlement and disputes.

## Overview / Responsibilities

Payment Orchestration is the fiat-facing component of the on-ramp saga. It sits
between the **Transaction Orchestrator** (the SAGA coordinator for the overall
end-to-end flow) and the per-rail **Rail Connectors**, presenting a single,
rail-agnostic API to the rest of the platform.

Core responsibilities:

- Normalize payment intents across supported fiat rails (card, ACH, SEPA, PIX, UPI).
- Orchestrate 3DS challenges for card-present / card-not-present flows.
- Execute the two-step auth + capture lifecycle for card rails.
- Ingest settlement, refund, and chargeback webhooks from each rail and reconcile
  them against stored intents.
- Maintain a durable, auditable state machine per payment intent.
- Act as the **SAGA coordinator for the payment sub-step** of the overall
  transaction saga, emitting compensating events on failure.
- Emit async events for every state transition to the Audit / Event Log.

## Language & Tech Stack

- **Language:** Go (standard library `net/http` or a thin router; strong
  concurrency primitives for in-flight webhook fan-in).
- **Idempotency keys:** every mutating endpoint accepts an `Idempotency-Key`
  header; replays return the cached result without re-executing side effects.
- **SAGA coordinator (payment sub-step):** owns the local
  `intent → authorized → captured → settled` progression and exposes
  compensating actions (refund / void) to the upstream Transaction Orchestrator.
- **Webhook ingestion:** durable, at-least-once delivery from each rail; every
  inbound webhook is persisted before processing and de-duplicated via
  rail-provided idempotency tokens (e.g. stripe-style `event_id`).
- **Persistence:** PostgreSQL as the system of record for intents and lifecycle
  events; row-level locking guards concurrent state transitions.
- **Observability:** structured logs, OpenTelemetry traces, metrics for p99
  intent creation and webhook backlog.

## System Requirements

The service must support the following functional requirements:

1. **Normalize payment intents across rails** — a single `POST /v1/payments/intents`
   accepts a rail-agnostic payload; the service translates it into the schema
   required by the target rail connector (card, ACH, SEPA, PIX, UPI).
2. **3DS challenge orchestration** — for card rails, initiate a 3DS challenge via
   the configured 3DS MPI, return the challenge artifact to the client, and resume
   authorization once the challenge result is posted back.
3. **Auth + capture two-step for cards** — separate authorization and capture
   calls; capture may be partial or final. Non-card rails map to the closest
   equivalent (e.g. instant rails collapse auth+capture into one step).
4. **Settlement webhook reconciliation** — ingest settlement notifications from
   each rail and reconcile captured amounts against settled amounts; surface
   breaks to Reconciliation.
5. **Chargeback / dispute handling** — accept dispute lifecycle events
   (chargeback opened, evidence submission window, arbitration, representment,
   reversal) and record them against the original intent.
6. **Refunds** — full and partial refunds, linked to the original capture; a
   refund is only allowed in terminal states (`captured`, `settled`).
7. **Idempotent intents** — duplicate intent creation with the same
   `Idempotency-Key` returns the original intent rather than creating a new one.
8. **Payment state machine** — every intent progresses through a well-defined
   state machine (see below); transitions are validated and persisted; invalid
   transitions are rejected with a `409 Conflict`.

## Non-Functional Requirements

- **Intent creation latency:** p99 < 100ms (excluding downstream rail latency for
  synchronous auth calls, which is bounded by per-rail SLOs).
- **Webhook ingestion durability:** at-least-once. Webhooks are persisted to
  PostgreSQL before processing; a worker drains the queue and applies them
  idempotently.
- **Idempotency:** all mutating endpoints honor an `Idempotency-Key` header.
  Replays within the key's TTL return the original response.
- **PCI-DSS scope minimization:** the service never stores, logs, or transmits
  PAN / track data. Card payloads are tokenized at the edge (or handled entirely
  by the rail connector / hosted fields). Payment Orchestration only ever holds
  rail-issued tokens and last-4 metadata.
- **Auditability:** every state transition is emitted as an event to the
  Audit / Event Log (async) and recorded locally in an append-only transitions
  table for forensics.
- **Availability:** stateless service horizontally scalable behind a load
  balancer; sessions/sticky routing not required.
- **Security:** mTLS between internal services; webhook signatures verified per
  rail before any processing.

## Technical Specifications

### API Surface

REST over HTTP/1.1 + JSON. All mutating requests accept an `Idempotency-Key`
header. Internal calls use mTLS; webhook endpoints are public but signature-gated.

### Endpoints

| Method & Path | Description |
|---|---|
| `POST /v1/payments/intents` | Create a normalized payment intent. Body includes `rail`, `amount`, `currency`, `payer_ref`, `rail-specific` fields. Returns the intent with an initial state of `intent`. |
| `GET /v1/payments/:id` | Fetch the current state and lifecycle history of an intent. |
| `POST /v1/payments/:id/capture` | Capture a previously authorized card intent. Supports partial capture via `amount`. |
| `POST /v1/payments/:id/refund` | Refund a captured/settled intent. Supports partial refund via `amount`. |
| `POST /v1/payments/:id/3ds-challenge` | Resume a card intent after the client completes a 3DS challenge. Body carries the challenge result / assertion. |
| `POST /v1/webhooks/:rail` | Inbound webhook ingestion for a specific rail (`card`, `ach`, `sepa`, `pix`, `upi`). Signature verified before processing. |

### Data Model

| Table | Purpose |
|---|---|
| `payment_intents` | Core intent record: `id`, `rail`, `amount`, `currency`, `payer_ref`, `state`, `idempotency_key`, `external_id`, timestamps. |
| `captures` | Capture records linked to an intent: `id`, `intent_id`, `amount`, `external_ref`, `captured_at`. |
| `settlements` | Settlement records from rail webhooks: `id`, `intent_id`, `capture_id`, `settled_amount`, `settled_at`, `rail_ref`. |
| `refunds` | Refund records: `id`, `intent_id`, `capture_id`, `amount`, `external_ref`, `refunded_at`, `state`. |
| `chargebacks` | Dispute records: `id`, `intent_id`, `amount`, `reason`, `stage` (`opened`/`evidence`/`arbitration`/`reversal`), `case_ref`, timestamps. |
| `webhooks` | Inbound webhook log: `id`, `rail`, `external_event_id`, `raw_payload`, `signature`, `processed_at`, `idempotency_token`. De-duplication key on `(rail, external_event_id)`. |

All tables use an append-only transitions pattern via an additional
`payment_transitions` table (`intent_id`, `from_state`, `to_state`,
`reason`, `actor`, `at`) for the audit trail.

### State Machine

```
                       ┌──────────────┐
                       │   intent     │
                       └──────┬───────┘
                              │ authorize (card) / submit (bank/instant)
                              ▼
                       ┌──────────────┐
        ┌──────────────│  authorized  │──────────────┐
        │              └──────┬───────┘              │
        │ void                │ 3DS challenge        │ refund (pre-capture)
        │                     │ required             │
        ▼                     ▼                      ▼
  ┌──────────┐         ┌──────────────┐        ┌──────────────┐
  │  voided  │         │ 3ds_pending  │────────▶│  authorized  │
  └──────────┘         └──────┬───────┘  resume └──────────────┘
                              │ failed / timed out
                              ▼
                       ┌──────────────┐
                       │   failed     │
                       └──────────────┘

  from `authorized`:
        │ capture
        ▼
  ┌──────────────┐  settle   ┌──────────────┐
  │   captured   │──────────▶│   settled    │
  └──────┬───────┘           └──────┬───────┘
         │ refund                    │ refund
         ▼                           ▼
  ┌──────────────┐            ┌──────────────┐
  │  refunding   │───────────▶│   refunded   │
  └──────────────┘            └──────────────┘

  from `captured` / `settled`:
        │ chargeback opened
        ▼
  ┌──────────────┐  representment won/lost  ┌──────────────────┐
  │ charged_back │ ────────────────────────▶│ chargeback_won  │
  └──────────────┘                          │ chargeback_lost │
                                            └──────────────────┘

Terminal states: voided, failed, refunded, charged_back,
                 chargeback_won, chargeback_lost.
```

### Integrations

| Direction | Service | Purpose |
|---|---|---|
| Outbound (sync) | **Rail Connectors** | Executes the rail-specific auth / capture / refund / submit calls. One connector per rail family, common interface. |
| Outbound (sync) | **Fraud Detection** | Scores each new intent before authorization; result gates whether auth proceeds. |
| Inbound (sync) | **Transaction Orchestrator** | Drives Payment Orchestration as a sub-step of the overall on-ramp saga; invokes capture / refund / void as needed. |
| Outbound (async) | **Audit / Event Log** | Receives an event for every payment state transition. |
| Outbound (async, consumed) | **Reconciliation** | Consumes settlement records to match Ledger against bank/rail state. |

### Idempotency

- All `POST` mutating endpoints accept an `Idempotency-Key` request header.
- The key is stored alongside the resulting intent/response and is unique per
  endpoint + key pair within a 24h TTL.
- A replay returns the original response body and status; no side effects are
  re-executed (no duplicate rail calls, no duplicate ledger events).
- For webhooks, the rail-supplied event id (e.g. `stripe_event_id`,
  `sepa_msg_id`) is used as the idempotency token; the `webhooks` table enforces
  uniqueness on `(rail, external_event_id)`.

### Webhook Security

- Every `POST /v1/webhooks/:rail` request is verified against a per-rail
  signature scheme before any processing:
  - **Card:** HMAC-SHA256 over the raw body with `WEBHOOK_SECRET_CARD`.
  - **ACH / SEPA:** IBAN/bank-specific signature schemes
    (`WEBHOOK_SECRET_ACH`, `WEBHOOK_SECRET_SEPA`).
  - **PIX / UPI:** bank-issued API-key + HMAC verification
    (`WEBHOOK_SECRET_PIX`, `WEBHOOK_SECRET_UPI`).
- Signature verification failure returns `401 Unauthorized` and the raw payload
  is never persisted to the `webhooks` table.
- A small replay-window check (±5 min on signed timestamp) guards against
  replayed webhook payloads.

## Dependencies

- **PostgreSQL** — system of record for intents, captures, settlements, refunds,
  chargebacks, webhooks, and transitions.
- **Rail Connectors** — one deployable per rail family; called synchronously for
  execution.
- **Fraud Detection** — called synchronously during intent creation/authorization.
- **Audit / Event Log** — async consumer of payment state-transition events.
- (Optional) **3DS MPI** — the 3DS Server / Merchant Plug-In used to initiate
  card challenges.
- (Optional) **Pricing / Quote** — consulted for fee/surcharge calculation where
  Payment Orchestration is responsible for surfacing totals to the client.

## Configuration

Configuration is purely environment-variable based (12-factor). Secrets are
injected via the platform secret manager at deploy time.

| Variable | Required | Description |
|---|---|---|
| `PORT` | yes | HTTP listen port (e.g. `8080`). |
| `DB_URL` | yes | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/payments?sslmode=require`. |
| `RAIL_CARD_URL` | yes | Base URL of the card Rail Connector. |
| `RAIL_ACH_URL` | yes | Base URL of the ACH Rail Connector. |
| `RAIL_SEPA_URL` | yes | Base URL of the SEPA Rail Connector. |
| `RAIL_PIX_URL` | no | Base URL of the PIX Rail Connector (enabled rails only). |
| `RAIL_UPI_URL` | no | Base URL of the UPI Rail Connector (enabled rails only). |
| `FRAUD_URL` | yes | Base URL of the Fraud Detection service. |
| `AUDIT_EVENT_LOG_URL` | yes | Base URL (or stream) of the Audit / Event Log. |
| `THREE_DS_MPI_URL` | yes (card rails) | Base URL of the 3DS MPI / 3DS Server. |
| `WEBHOOK_SECRET_CARD` | yes | HMAC secret for verifying card-rail webhooks. |
| `WEBHOOK_SECRET_ACH` | yes | Secret for verifying ACH webhooks. |
| `WEBHOOK_SECRET_SEPA` | yes | Secret for verifying SEPA webhooks. |
| `WEBHOOK_SECRET_PIX` | no | Secret for verifying PIX webhooks. |
| `WEBHOOK_SECRET_UPI` | no | Secret for verifying UPI webhooks. |
| `IDEMPOTENCY_KEY_TTL` | no | TTL for idempotency key retention (default `24h`). |
| `WEBHOOK_REPLAY_WINDOW` | no | Allowed clock skew for signed webhook timestamps (default `5m`). |
| `LOG_LEVEL` | no | One of `debug`/`info`/`warn`/`error` (default `info`). |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | OpenTelemetry OTLP endpoint for traces/metrics. |
| `MTLS_CERT_FILE` / `MTLS_KEY_FILE` / `MTLS_CA_FILE` | yes | mTLS material for internal service-to-service calls. |

## Local Development

```sh
# Build
go build -o bin/payment-orchestration ./cmd/payment-orchestration

# Run (requires PostgreSQL + Rail Connectors + Fraud Detection reachable)
PORT=8080 \
DB_URL=postgres://postgres:postgres@localhost:5432/payments?sslmode=disable \
RAIL_CARD_URL=http://localhost:9001 \
RAIL_ACH_URL=http://localhost:9002 \
RAIL_SEPA_URL=http://localhost:9003 \
FRAUD_URL=http://localhost:9100 \
AUDIT_EVENT_LOG_URL=http://localhost:9200 \
THREE_DS_MPI_URL=http://localhost:9300 \
WEBHOOK_SECRET_CARD=dev-secret-card \
WEBHOOK_SECRET_ACH=dev-secret-ach \
WEBHOOK_SECRET_SEPA=dev-secret-sepa \
./bin/payment-orchestration

# Test
go test ./... -race -cover

# Lint
golangci-lint run ./...
```