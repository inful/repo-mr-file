# updateext

Replaces `create-bundle-mr.sh` (a 177-line bash + curl + grep-JSON script) with a
single static Go binary that creates or updates a GitLab merge request which
delivers an updated CA certificate bundle to an external repository.

## Install

```bash
go install github.com/inful/updateext/cmd/create-bundle-mr@latest
```

…or build from source:

```bash
go build -trimpath -ldflags="-s -w" -o dist/create-bundle-mr ./cmd/create-bundle-mr
```

## Usage

```bash
create-bundle-mr \
  --tag=v1.2.3 \
  --repo=some-group/some-project \
  --cert-path=path/inside/repo/ca-bundle.crt \
  --bundle=/local/path/to/source-bundle.pem
```

…or rely on env vars for the values that have them:

| Flag            | Env var          | Default                                       |
|-----------------|------------------|-----------------------------------------------|
| `--tag`         | —                | (required)                                    |
| `--repo`        | —                | (required)                                    |
| `--cert-path`   | —                | (required)                                    |
| `--bundle`      | —                | (required)                                    |
| `--gitlab-api`  | `GITLAB_API`     | `https://gitlab.mgmlab.net/api/v4`            |
| `--gitlab-url`  | `GITLAB_URL`     | derived from `--gitlab-api`                   |
| `--gitlab-token`| `GITLAB_TOKEN`   | (required)                                    |
| `--target-branch` | `TARGET_BRANCH`| project default branch                        |
| `--branch-name` | —                | `chore/update-ca-bundle-${TAG}`               |
| `--commit-message` | —            | `chore: update CA certificate bundle from custom-certs ${TAG}` |
| `--mr-title`    | —                | derived from `--commit-message`               |
| `--mr-description` | —            | templated                                     |
| `--retries`     | —                | `3`                                           |
| `--retry-backoff` | —             | `500ms` (exponential with jitter)             |
| `--log-format`  | —                | `text` (`text` \| `json`)                     |
| `--verbose`     | —                | `false`                                       |
| `--dry-run`     | —                | `false`                                       |

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | Success (file written + MR created, or no-op)      |
| `2`  | Configuration error (missing flag, token, source)   |
| `3`  | Auth / permission error (401, 403)                 |
| `4`  | Not found (404 on project)                         |
| `5`  | Conflict (409 — stale file branch head)            |
| `6`  | Transient failure exhausted (5xx / 429)            |
| `7`  | Unexpected internal error                          |

## Development

### One-time bootstrap

```bash
# install lefthook (https://github.com/evilmartians/lefthook) and enable the hook
lefthook install
```

### Day-to-day commands

```bash
go test -race -coverprofile=coverage.out ./...      # run all tests with race detector
go vet ./...                                       # quick vet pass
golangci-lint run # full lint
go build -trimpath -ldflags="-s -w" -o dist/create-bundle-mr ./cmd/create-bundle-mr  # build static binary
go mod tidy                                        # tidy module dependencies
```

The `lefthook` pre-commit hook runs `golangci-lint run` and
`go test -race -count=1 ./...` on staged changes.

### TDD workflow

Every code change follows: red (failing test) → green (impl) → refactor → tests
green → lint clean → conventional commit. Commit messages follow the
[Conventional Commits](https://www.conventionalcommits.org/) specification
(`feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `chore:`, `build:`, `ci:`).

## Migration from `create-bundle-mr.sh`

The bash script remains in the repo as a rollback during cutover. The new
binary is a behavioral superset:

- The bash script's `$3`/`$4` argument-binding typo is corrected.
- The bash script's always-null `last_commit_id` is replaced with the value
  read from the file API, so stale-branch conflicts surface as a typed 409
  instead of a confusing 400.
- `cmp -s` becomes `bytes.Equal`.
- Brittle `grep -o '"id":[0-9]*'` JSON parsing is replaced with the official
  GitLab client's typed responses.
- 5xx / 429 responses now retry with exponential backoff and respect
  `Retry-After`.
- Exit codes distinguish failure classes so CI can branch appropriately.

All other behaviors are preserved: idempotent retry (existing MR is reused),
bundle-matches-skip, source-branch-equals-target short-circuit, and the same
branch / commit-message / MR-title / MR-description templates.