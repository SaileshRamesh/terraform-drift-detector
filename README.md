# tfdrift: Terraform Drift Detection

`tfdrift` compares Terraform state files against live cloud infrastructure and
reports drift: deleted resources, modified attributes, tag changes, and
(optionally) resources that exist in the cloud but in no state file. It reads
state JSON and calls cloud APIs directly. It never runs `terraform plan`,
needs no provider binaries, and needs only read permissions.

```
Terraform state ──► State Reader ──► Resource Extractor ──► Expected model ─┐
                                                                            ├─► Drift Engine ──► Report ──► console / JSON / dashboard
Cloud APIs ───────► Cloud Fetcher ──► Resource Extractor ──► Actual model ──┘
```

One static binary provides the CLI, a REST API, a web dashboard, scheduled
scans and SQLite-backed scan history.

## Quick start

```sh
make build

# One-off scans
./bin/tfdrift scan --state ./terraform.tfstate --region us-east-1
./bin/tfdrift scan --state s3://my-tf-state/prod/terraform.tfstate --format json
./bin/tfdrift scan --state ./terraform.tfstate --detect-unmanaged --fail-on-drift

# What will be compared (reads state only, no cloud calls)
./bin/tfdrift inventory --state testdata/terraform.tfstate

# Dashboard + API + schedules
cp examples/drift.yaml drift.yaml   # edit sources/workspaces
./bin/tfdrift serve                 # http://127.0.0.1:8080
```

AWS credentials come from the standard chain (env vars, `--profile`, SSO,
instance/task role).

## Commands

| Command | Purpose |
|---|---|
| `scan` | Scan the default workspace, `--workspace <name>`, or `--state` sources. Records history when a database is configured |
| `serve` | REST API, dashboard and cron scheduler |
| `history` | List recorded scans (`--workspace`, `--limit`) |
| `report <scan-id>` | Show a recorded scan (`--format console\|json`) |
| `workspace list\|show\|delete` | Inspect workspaces (config- and API-defined) |
| `schedule set <ws> <cron>` / `schedule remove <ws>` | Manage schedules of API-defined workspaces; a running server picks changes up within a minute |
| `inventory` | Show the expected model extracted from state, without calling the cloud |
| `handlers` | List supported resource types |

Global flags: `--config` (default `./drift.yaml`), `--db` (SQLite path).

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Scan completed (no drift, or drift without `--fail-on-drift`) |
| 1 | Scan could not run (bad config, unreadable state, no credentials) |
| 2 | Drift detected and `--fail-on-drift` set. Use as a CI gate |

## Configuration

See [`examples/drift.yaml`](examples/drift.yaml). In short:

- **State sources**: `path`, `s3: {bucket, key, region}`, or `url` (http/https,
  credentials may be embedded and are redacted in reports).
- **Workspaces**: named targets with their own sources, AWS settings, ignore
  rules, `detect_unmanaged` and cron `schedule`. They inherit top-level `aws`
  and `ignore`. Workspaces defined in the file are read-only through the
  API/CLI; edit the file instead. Workspaces created through the API are
  stored in the database.
- **Ignore rules**: glob patterns per resource type; tags are `tags.<key>`.
  AWS-reserved `aws:*` tags (CloudFormation, Auto Scaling...) are always
  skipped, since neither users nor Terraform can set them.

## Drift kinds

| Kind | Severity | Meaning |
|---|---|---|
| `DELETED` | high | In state, not in the cloud |
| `MODIFIED` | medium (high for security groups / IAM) | Compared attribute differs |
| `TAGS_CHANGED` | low | Tags added, removed or changed outside Terraform |
| `UNMANAGED` | medium | In the cloud, in none of the scanned states (opt-in) |
| `ERROR` | medium | Could not fetch (e.g. missing IAM permission); drift unknown |
| `SKIPPED` | info | Resource type has no handler yet |

`UNMANAGED` detection lists every resource of the types present in the
scanned state, in the regions present in it. It skips AWS-created defaults
(default VPC, subnets and security groups). Scan all state files for an
account in one workspace, or resources managed by another state will show up
as unmanaged. IAM roles are not listed, because service-linked roles would
drown real findings.

## REST API

| Method | Endpoint | Description |
|---|---|---|
| GET | `/health` | Health check |
| GET | `/api/v1/workspaces` | Workspaces with next run, running scan and last result |
| POST | `/api/v1/workspaces` | Create a workspace (JSON body, same shape as the config) |
| GET / PUT / DELETE | `/api/v1/workspaces/{name}` | Read, create-or-replace, delete |
| POST | `/api/v1/workspaces/{name}/scans` | Start a scan. Returns `202 {scan_id}`, or `409` if one is running |
| GET | `/api/v1/workspaces/{name}/scans` | Scan history for a workspace |
| PUT / DELETE | `/api/v1/workspaces/{name}/schedule` | Set `{"cron": "..."}` or remove the schedule |
| GET | `/api/v1/scans?workspace=&limit=` | Recent scans |
| GET | `/api/v1/scans/{id}` | Full report |
| GET | `/api/v1/scans/{id}/report?format=json\|text` | Report rendered as JSON or console text |

Set `api.api_key` to require `X-API-Key: <key>` or `Authorization: Bearer
<key>` on `/api/`. The server listens on `127.0.0.1:8080` by default and warns
when bound to a non-loopback address without a key.

## Supported resources (AWS)

| Type | Compared | Unmanaged listing |
|---|---|---|
| `aws_instance` | ami, instance_type, subnet, private IP, key, instance profile, security groups, ebs_optimized, monitoring, source_dest_check, tags | ✓ |
| `aws_security_group` | name, description, vpc, ingress/egress rules (order-insensitive), tags | ✓ |
| `aws_vpc` | cidr_block, instance_tenancy, tags | ✓ |
| `aws_subnet` | vpc, cidr_block, ipv6_cidr_block, availability_zone, map_public_ip_on_launch, assign_ipv6_address_on_creation, tags | ✓ |
| `aws_s3_bucket` | existence, tags | ✓ |
| `aws_iam_role` | path, description, max session, permissions boundary, trust policy (semantic JSON compare), tags | – |

Minimum read-only IAM permissions: `ec2:DescribeInstances`,
`ec2:DescribeSecurityGroups`, `ec2:DescribeVpcs`, `ec2:DescribeSubnets`,
`s3:ListBucket` (HeadBucket), `s3:GetBucketTagging`, `iam:GetRole`,
`s3:ListAllMyBuckets` (unmanaged detection), and `s3:GetObject` on S3 state
files.

## Adding a resource type

Implement `cloud.Handler` (see `internal/cloud/awscloud/vpc.go` for the
smallest example) and add it to `awscloud.Handlers`. `FromState` and
`FetchActual` must emit identical attribute keys and value types: strings,
bools, `int64`, sorted `[]string`. Normalize on both sides (e.g. ARN → name,
URL-decoded policies) so equal values compare equal. Implement `cloud.Lister`
as well to support unmanaged detection.

## Development

```sh
make test    # unit tests; no cloud account needed (API clients are faked)
make lint    # gofmt + go vet
make run-server
```

See [docs/PLAN.md](docs/PLAN.md) for the architecture and roadmap.
