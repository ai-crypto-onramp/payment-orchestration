# Project Plan — Payment Orchestration

This document breaks the implementation of the Payment Orchestration service
into ordered, independently shippable stages. Each stage lists a goal, a tasks
checklist, and acceptance criteria. Stages are ordered so that foundational
pieces (schema, idempotency, state machine) land before the features that
depend on them. See `README.md` for the full system specification.

## Stage 1 — Database schema for intents, captures, settlements, refunds, chargebacks, webhooks

### Goal

Establish the PostgreSQL system of record described in the README data model:
`payment_intents`, `captures`, `settlements`, `refunds`, `chargebacks`,
`webhooks`, and the append-only `payment_transitions` audit table.

### Tasks

- [ ] Define migrations for `payment_intents` (id, rail, amount, currency, payer_ref, state, idempotency_key, external_id, timestamps).
- [ ] Define migrations for `captures` (id, intent_id, amount, external_ref, captured_at).
- [ ] Define migrations for `settlements` (id, intent_id, capture_id, settled_amount, settled_at, rail_ref).
- [ ] Define migrations for `refunds` (id, intent_id, capture_id, amount, external_ref, refunded_at, state).
- [ ] Define migrations for `chargebacks` (id, intent_id, amount, reason, stage, case_ref, timestamps).
- [ ] Define migrations for `webhooks` (id, rail, external_event_id, raw_payload, signature, processed_at, idempotency_token) with unique constraint on `(rail, external_event_id)`.
- [ ] Define migrations for `payment_transitions` (id, intent_id, from_state, to_state, reason, actor, at) as append-only.
- [ ] Add indexes on `payment_intents(idempotency_key)`, `webhooks(rail, external_event_id)`, and FK indexes on `intent_id`.
- [ ] Add a Go migration runner (e.g. `golang-migrate`) wired into `cmd/payment-orchestration`.

### Acceptance criteria

- `go test ./...` passes against a throwaway Postgres container with all migrations applied.
- All seven tables exist with the columns listed in the README data model.
- `webhooks` enforces uniqueness on `(rail, external_event_id)`; duplicate insert fails.
- `payment_transitions` rejects `UPDATE`/`DELETE` (trigger or app-level guard documented).

## Stage 2 — Intent creation endpoint + idempotency

### Goal

Implement `POST /v1/payments/intents` and `GET /v1/payments/:id` with the
`Idempotency-Key` semantics described in the README (24h TTL, replay returns
the cached response, no duplicate side effects).

### Tasks

- [ ] Scaffold the HTTP server in `cmd/payment-orchestration` with a thin router.
- [ ] Implement `POST /v1/payments/intents` accepting rail-agnostic payload (rail, amount, currency, payer_ref, rail-specific fields).
- [ ] Implement `Idempotency-Key` header handling: persist key with the intent, return cached response on replay within TTL.
- [ ] Implement `GET /v1/payments/:id` returning current state + lifecycle history from `payment_transitions`.
- [ ] Validate rail enum (card, ach, sepa, pix, upi); reject unknown rails with `400`.
- [ ] Reject missing `Idempotency-Key` on the POST with `400`.
- [ ] Add row-level locking helpers for `payment_intents` (used later by the state machine).

### Acceptance criteria

- Duplicate POST with the same `Idempotency-Key` returns the original intent and does not insert a second row.
- `GET /v1/payments/:id` returns `404` for unknown ids and the full lifecycle for known ones.
- p99 intent creation latency < 100ms on a local Postgres (excluding downstream rail calls).

## Stage 3 — 3DS challenge orchestration

### Goal

For card rails, initiate a 3DS challenge via the configured 3DS MPI, return the
challenge artifact to the client, and resume authorization when the client
posts the challenge result back (`POST /v1/payments/:id/3ds-challenge`).

### Tasks

- [ ] Add a 3DS MPI client (`THREE_DS_MPI_URL`) with a `Challenge(intent)` and `Resume(intent, assertion)` interface.
- [ ] Implement the `3ds_pending` state transition from `authorized` when a challenge is required.
- [ ] Implement `POST /v1/payments/:id/3ds-challenge` to resume: validate assertion, transition `3ds_pending -> authorized` on success or `3ds_pending -> failed` on failure/timeout.
- [ ] Return the challenge artifact (ACS URL / payload) in the intent creation response when a challenge is required.
- [ ] Enforce the 3DS timeout window and transition to `failed` on expiry.
- [ ] Add idempotency on the resume endpoint.

### Acceptance criteria

- A card intent requiring 3DS returns a challenge artifact and lands in `3ds_pending`.
- Successful resume moves the intent to `authorized`; failed/timeout moves it to `failed`.
- Replaying the same `3ds-challenge` request with the same key returns the cached result without re-calling the MPI.

## Stage 4 — Auth + capture two-step for cards

### Goal

Implement the card two-step lifecycle: authorization (synchronous rail call
during/after intent creation) and `POST /v1/payments/:id/capture` (full or
partial). Non-card rails map to the closest equivalent (instant rails collapse
auth+capture into a single submit).

### Tasks

- [ ] Add a `RailConnector` interface with `Authorize`, `Capture`, `Refund`, `Submit`, `Void` methods.
- [ ] Implement synchronous auth call for card rails, persist `external_id` on the intent.
- [ ] Implement `POST /v1/payments/:id/capture` supporting `amount` (partial) and final capture.
- [ ] Record `captures` rows on each capture with `external_ref` and `captured_at`.
- [ ] Collapse auth+capture into a single `Submit` for instant rails (PIX, UPI) and ACH/SEPA.
- [ ] Support `void` from `authorized` -> `voided` and pre-capture `refund`.
- [ ] Honor `Idempotency-Key` on the capture endpoint.

### Acceptance criteria

- A card intent can move `intent -> authorized -> captured` with partial and final captures summing correctly.
- Non-card rails reach `captured` via a single submit.
- Void from `authorized` transitions to `voided` and rejects further captures.
- Duplicate capture request with the same idempotency key does not double-capture.

## Stage 5 — Rail connectors integration

### Goal

Wire the per-rail connectors (card, ACH, SEPA, PIX, UPI) behind the common
`RailConnector` interface, with mTLS outbound transport and per-rail
configuration (`RAIL_*_URL`).

### Tasks

- [ ] Implement HTTP clients for each rail connector using the common interface.
- [ ] Configure mTLS transport (`MTLS_CERT_FILE` / `MTLS_KEY_FILE` / `MTLS_CA_FILE`) for all outbound internal calls.
- [ ] Add per-rail config loading from env (`RAIL_CARD_URL`, `RAIL_ACH_URL`, `RAIL_SEPA_URL`, `RAIL_PIX_URL`, `RAIL_UPI_URL`).
- [ ] Map rail-specific payload shapes from the normalized intent into each connector's expected schema.
- [ ] Add retry/timeout policies per rail with bounded SLOs.
- [ ] Integrate Fraud Detection (`FRAUD_URL`) call before authorization; gate auth on the fraud result.

### Acceptance criteria

- All configured rails can execute auth/capture/submit/refund via their connector.
- Outbound calls use mTLS with the configured material.
- Fraud score is fetched before every authorization and blocks disallowed intents.
- Unknown/disabled rails return a clear `400` at intent creation.

## Stage 6 — Settlement webhook reconciliation

### Goal

Ingest settlement notifications from each rail via `POST /v1/webhooks/:rail`,
persist them durably before processing, de-duplicate via rail event ids, and
reconcile captured vs settled amounts, surfacing breaks to Reconciliation.

### Tasks

- [ ] Implement `POST /v1/webhooks/:rail` for all rails with signature verification (HMAC-SHA256 for card, per-rail schemes for banks).
- [ ] Persist every inbound webhook to `webhooks` before processing; de-dup on `(rail, external_event_id)`.
- [ ] Add a webhook worker that drains the persisted queue and applies events idempotently.
- [ ] Map settlement events to `settlements` rows and transition the intent `captured -> settled`.
- [ ] Reconcile `captures.amount` vs `settlements.settled_amount`; emit a reconciliation break event on mismatch.
- [ ] Enforce the `WEBHOOK_REPLAY_WINDOW` (±5 min) on signed timestamps; reject old payloads with `401`.
- [ ] Return `401` on signature failure and never persist the raw payload.

### Acceptance criteria

- Duplicate webhook with the same `external_event_id` is processed exactly once.
- Signature/replay-window failures return `401` and leave no row in `webhooks`.
- Settlement webhook moves `captured -> settled` and records a `settlements` row.
- Amount breaks are emitted to the Audit/Event Log and flagged for Reconciliation.

## Stage 7 — Refund + chargeback handling

### Goal

Implement `POST /v1/payments/:id/refund` (full and partial, linked to the
original capture) and chargeback/dispute ingestion via webhooks, recording the
full dispute lifecycle (opened, evidence, arbitration, representment,
reversal).

### Tasks

- [ ] Implement `POST /v1/payments/:id/refund` supporting partial `amount`; allow only from terminal `captured`/`settled` states.
- [ ] Create `refunds` rows linked to the originating `capture_id`; call the rail `Refund` method.
- [ ] Transition intent `captured/settled -> refunding -> refunded` and emit the transition event.
- [ ] Ingest chargeback webhook events and create `chargebacks` rows (amount, reason, stage, case_ref).
- [ ] Transition intent `captured/settled -> charged_back` on dispute opened.
- [ ] Track dispute stage progression (`opened -> evidence -> arbitration -> reversal`) and end in `chargeback_won` / `chargeback_lost`.
- [ ] Enforce idempotency on the refund endpoint and on chargeback webhook processing.

### Acceptance criteria

- Refund from a non-terminal state is rejected with `409 Conflict`.
- Partial refunds sum correctly and never exceed the captured amount.
- A chargeback opened on a settled intent moves it to `charged_back` and records the dispute.
- Dispute stage progression ends in `chargeback_won` or `chargeback_lost` and is fully audited.

## Stage 8 — State machine + audit emission

### Goal

Formalize the payment state machine from the README, validate every transition
under row-level locking, reject invalid transitions with `409 Conflict`, and
emit an async event to the Audit / Event Log for every transition plus record
it locally in `payment_transitions`.

### Tasks

- [ ] Implement a state machine module encoding all allowed transitions from the README diagram.
- [ ] Validate transitions under `SELECT ... FOR UPDATE` on the intent row; reject invalid with `409`.
- [ ] Append a row to `payment_transitions` for every transition (from_state, to_state, reason, actor, at).
- [ ] Emit an async event to `AUDIT_EVENT_LOG_URL` for every transition with intent id, states, and metadata.
- [ ] Define and enforce terminal states (`voided`, `failed`, `refunded`, `charged_back`, `chargeback_won`, `chargeback_lost`); reject further transitions from terminal.
- [ ] Wire the state machine into all endpoints from Stages 2-7.

### Acceptance criteria

- Every invalid transition returns `409 Conflict` with a descriptive body.
- Every successful transition produces exactly one `payment_transitions` row and one audit event.
- Concurrent transitions on the same intent are serialized via row-level locking; no lost updates.
- Terminal states reject all further mutations.

## Stage 9 — Observability

### Goal

Add structured logging, OpenTelemetry traces and metrics, and health/readiness
endpoints, covering p99 intent creation latency and webhook backlog as called
out in the README non-functional requirements.

### Tasks

- [ ] Add structured logging (`LOG_LEVEL` respected) across handlers, workers, and connectors.
- [ ] Add OpenTelemetry tracing exported to `OTEL_EXPORTER_OTLP_ENDPOINT`; span per HTTP request and per rail call.
- [ ] Add metrics: p99 intent creation latency, webhook backlog/queue depth, transition counters per state.
- [ ] Add `GET /healthz` (liveness) and `GET /readyz` (readiness, gated on DB ping).
- [ ] Add request logging middleware with redaction that never logs PAN/card tokens.
- [ ] Wire trace context propagation to outbound rail/fraud/audit calls.

### Acceptance criteria

- Logs are structured JSON, respect `LOG_LEVEL`, and contain no PAN or card token data.
- Traces reach the configured OTLP endpoint with spans for HTTP and outbound calls.
- p99 intent latency and webhook backlog metrics are exposed and scrapeable.
- `/healthz` and `/readyz` behave correctly when the DB is unreachable.

## Stage 10 — Tests, coverage, and Docker

### Goal

Reach high test coverage with unit + integration tests, enforce lint and race
detection, and provide a reproducible Docker image and local dev setup.

### Tasks

- [ ] Add unit tests for state machine, idempotency, signature verification, and rail payload mapping.
- [ ] Add integration tests against a throwaway Postgres covering the full lifecycle (intent -> auth -> 3DS -> capture -> settle -> refund / chargeback).
- [ ] Add webhook dedup and replay-window tests.
- [ ] Enforce `go test ./... -race -cover` and `golangci-lint run ./...` in CI.
- [ ] Update Dockerfile for a multi-stage build producing a minimal runtime image.
- [ ] Add a `docker-compose` for local dev (Postgres + service + mock rails).
- [ ] Document local dev workflow in README.

### Acceptance criteria

- `go test ./... -race -cover` passes with coverage ≥ 80%.
- `golangci-lint run ./...` is clean.
- `docker build` produces a working image; `docker-compose up` brings the service up healthy against local Postgres.
- CI badge is green on `main`.