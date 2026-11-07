# Plan & Architecture

## Goals

Continuously compare Terraform state with live infrastructure, without
`terraform plan`/`apply`; cloud-agnostic, extensible, fast. Output via CLI,
JSON and (later) a dashboard; scans on demand or on a schedule.

## Stack

Go · cobra (CLI) · aws-sdk-go-v2 · yaml.v3 · errgroup · modernc.org/sqlite
(pure Go, so the binary stays static and cgo-free) · robfig/cron · net/http
with an embedded vanilla-JS dashboard (no frontend build step).

## Architecture → packages

| Diagram box | Package |
|---|---|
| Terraform State | `internal/state` `Reader`: `LocalReader`, `S3Reader`, `HTTPReader` |
| State Reader / Resource Extractor | `state.Parse`, `state.Extract` |
| Cloud Provider / Cloud Fetcher | `internal/cloud/awscloud` (`SDKClients`, batching, retries) |
| Resource Extractor (cloud) | one `cloud.Handler` per Terraform type |
| Expected / Actual Resource Model | `internal/model.Resource` |
| Drift Engine | `internal/engine.Differ` (+ `internal/normalize`) |
| Report Generator | `internal/report` (console, JSON) |
| Orchestration | `internal/scan.Run` (one scan), `internal/runner` (workspace scans + persistence) |
| Scheduling | `internal/scheduler` (cron, overlap-safe, re-syncs from the DB every minute) |
| History | `internal/store` (SQLite: workspaces, scans) |
| API + dashboard | `internal/server` (`tfdrift serve`) |

Key design choices:

- **Handler per resource type** is the extension point: it owns both
  normalizations (state → model, API → model) for a curated attribute set, so
  computed/noisy attributes never produce false drift.
- **Batched, concurrent fetching**: resources are grouped by (type, region);
  EC2 uses `Describe*` with id *filters* (200 per call) so a deleted id does
  not fail the batch; APIs without batch lookup run with bounded parallelism.
- **Unmanaged detection is opt-in**: handlers implementing `cloud.Lister`
  enumerate their type per region; AWS-created defaults are excluded.
- **Config vs API workspaces**: drift.yaml workspaces are synced into the DB
  and are read-only via API/CLI; API-created ones live only in the DB.
- **Partial failure is visible**: a failed group becomes `ERROR` entries, not
  an aborted scan; unsupported types become `SKIPPED`.

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| 1. MVP | Model, local + S3 state, AWS handlers, differ, ignore rules, console + JSON, CLI, exit codes, tests | **done** |
| 2. Platform | `tfdrift serve`: REST API, workspaces, SQLite history, cron schedules, dashboard, API key; HTTP state backend; `aws_subnet`; UNMANAGED detection; `inventory`, `history`, `report`, `workspace`, `schedule` commands | **done** |
| 3. More coverage | More AWS handlers (route table, RDS, Lambda, S3 sub-resources, IAM policy); Azure + GCP providers and Azure Blob / GCS state backends | next |
| 4. Hardening | Slack/webhook notifications, multi-account (assume role), history retention, Docker image, SSO for the dashboard | |
