# multi-tour-workflow-register-payment

> Workflow saga register-payment (commercial-reservation), with compensations

Part of the **Multi Tour** distributed system — team `multi-tour`, Grupo 2.
Governance and documentation live in [`multi-tour-docs`](https://github.com/code-corhuila/multi-tour-docs).

## What it is

The saga of HU-WFL-002 (ADR-004 D3): it records the income in the cash first, then records the validated payment in the reservation, and reverses the income with a compensating movement if the second step fails. Its state is saved after every step. Spec (PROPUESTO, not signed yet): `07-api/specs/commercial-reservation/multi-tour-workflow-register-payment/` in `multi-tour-docs`.

## How to start it

Run the migrator once per environment (see `deploy/compose.yml`), then `docker compose -f deploy/compose.yml up workflow-register-payment`, with the variables of `.env.example` and the `SERVICE_TOKEN` issued by `multi-tour-security`. The service listens on 8080 inside the platform network and publishes no port.

## Where the data is

In PostgreSQL, schema `workflow_register_payment` of the single instance (ADR-005 E4); its migrations live in `db/` of this repository.

## How to test it

`gofmt -l .` must print nothing, then `go vet ./...` and `go test ./...` (Go 1.26). The same three run in `ci.yml` on pull requests to `develop` and `qa`, with a PostgreSQL service for `TEST_DATABASE_URL` and the `db-ci` script for the migrations.

## What is missing

The steps, the participant clients and the entry route (`POST /api/v1/sagas/register-payment`) are tasks WF2-02 and WF2-03; the domain of the saga is part 3 of WF2-01.

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
