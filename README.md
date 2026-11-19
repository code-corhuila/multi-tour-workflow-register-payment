# multi-tour-workflow-register-payment

> Workflow saga register-payment (commercial-reservation), with compensations

Part of the **Multi Tour** distributed system — team `multi-tour`, Grupo 2.
Governance and documentation live in [`multi-tour-docs`](https://github.com/code-corhuila/multi-tour-docs).

## What it is

The saga of HU-WFL-002 (ADR-004 D3): it records the income in the cash first, then records the validated payment in the reservation, and reverses the income with a compensating movement if the second step fails. Its state is saved after every step. Spec (APPROVED, signed 2026-11-14): `07-api/specs/commercial-reservation/multi-tour-workflow-register-payment/` in `multi-tour-docs`.

It is implemented: the steps, the participant clients (cash-reporting and commercial-reservation) and the entry route.

- Route: `POST /api/v1/sagas/register-payment` starts a saga (Bearer token of a Tenant Administrator or a Collaborator with the right, header `Idempotency-Key` of 8 to 128 characters, JSON body); `GET /api/v1/sagas/register-payment/{id}` reads its state; `GET /health`.
- Idempotency key per tenant: the key is unique per tenant (`uq_register_payment_saga_tenant_idempotency_key`), so a repeated key returns the saga already started and never reveals another tenant's. Another saga for the same payment is answered `409 PAYMENT_ALREADY_REGISTERED`.
- Retries: each participant call is retried with full jitter after a network failure, 429 or 5xx, up to `STEP_ATTEMPTS`; every write carries the step key as `Idempotency-Key`, so a retry is the same intention. Any other 4xx is a definitive rejection.
- Option B: when the outcome of a step is unknown after the retries, the saga reads the participant before deciding. Unknown income: it asks the cash whether the income exists; unclear payment: it asks the reservation whether the payment is validated (validated completes, not validated reverses the income, no answer ends FAILED for a person).
- Resume: a RUNNING saga not moved for longer than the saga bound is claimed and finished from its saved state; the sweep repeats every bound.

## How to start it

Run the migrator once per environment (see `deploy/compose.yml`), then `docker compose -f deploy/compose.yml up workflow-register-payment`, with the variables of `.env.example` and the `SERVICE_TOKEN` issued by `multi-tour-security`. The service listens on 8080 inside the platform network and publishes no port.

## Where the data is

In PostgreSQL, schema `workflow_register_payment` of the single instance (ADR-005 E4); its migrations live in `db/` of this repository.

## How to test it

`gofmt -l .` must print nothing, then `go vet ./...` and `go test ./...` (Go 1.26). The same three run in `ci.yml` on pull requests to `develop` and `qa`, with a PostgreSQL service for `TEST_DATABASE_URL` and the `db-ci` script for the migrations.

The tests that touch the state store need a PostgreSQL: set `TEST_DATABASE_URL` to an instance whose user may create a database (they create a throwaway one and apply `db/01_ddl`); without it they are skipped locally and fail in the CI. `sh db/tests/db-ci.sh` builds the schema from an empty PostgreSQL, reruns it, rolls it back and rebuilds it.

To try it by hand: start the saga as in "How to start it", then `curl -X POST http://<host>:8080/api/v1/sagas/register-payment -H "Authorization: Bearer <token>" -H "Idempotency-Key: <8 to 128 characters>" -H "Content-Type: application/json" -d @payment.json` from inside the platform network (the service publishes no port) and read the result with the `GET` of the returned id. The body fields are in `openapi/`.

## Branching

Three permanent branches. **None of them accepts a direct commit** — you enter through a child
branch and leave through a Pull Request.

```
develop  <--PR--  feat/... fix/... chore/...
qa       <--PR--  qa/...
main     <--PR--  release/...  hotfix/...
```

Promotion happens **by re-application** (`git cherry-pick -x`), never by merging one permanent
branch into another: `merge develop -> qa` and `merge qa -> main` do not exist in this model.

`main` requires **1 approval from `ariel5253`**. On `develop` and `qa` the team sets its own review
rule.

Full policy: `00-governance/branching-policy.md` in `multi-tour-docs`.
