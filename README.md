# repo-mr-file

Adds or updates a single file in a GitLab, GitHub, Gitea, or Forgejo
repository and ensures an open merge/pull request exists. The tool is
project-agnostic — it doesn't care what's in the file, so it works
equally well for cert bundles, IP allow lists, tool versions,
configuration snippets, or any other text blob you need to publish
through a merge request (MR) or pull request (PR).

## Install

Pre-built binaries (linux/darwin/windows × amd64/arm64):

```bash
# Linux/macOS — see the release page for the full per-version URL
curl -L -o repo-mr-file.tar.gz \
  https://github.com/inful/repo-mr-file/releases/latest/download/repo-mr-file_<ver>_linux_amd64.tar.gz
tar -xzOf repo-mr-file.tar.gz repo-mr-file > /usr/local/bin/repo-mr-file
chmod +x /usr/local/bin/repo-mr-file
```

…or `go install`:

```bash
go install github.com/inful/repo-mr-file/cmd/repo-mr-file@latest
```

…or pull the multi-arch container image:

```bash
# Multi-arch manifest resolves to linux/amd64 or linux/arm64 automatically.
docker pull ghcr.io/inful/repo-mr-file:latest
docker run --rm ghcr.io/inful/repo-mr-file:latest --help
```

Two image variants are published per release:

| Tag suffix | Base image | Shell | Size (approx) | Use case |
|---|---|---|---|---|
| `:vX.Y.Z` (or `:latest`) | `gcr.io/distroless/static:nonroot` | none | ~5 MB | Production. Default for `docker run` and Kubernetes. |
| `:vX.Y.Z-debug` (or `:latest-debug`) | `gcr.io/distroless/static:debug-nonroot` | busybox | ~8 MB | CI jobs that want `ls` / `cat` / `env` available alongside the binary, or operators who need `docker exec -it <container> /busybox/sh` for debugging. |

The debug image has the same binary, the same `nonroot` UID, the
same OCI labels, and the same `ENTRYPOINT`. It is a drop-in
replacement for the production image when the user explicitly
opts in with the `-debug` tag. Use it in CI pipelines like:

```yaml
# .gitlab-ci.yml (or any container-based CI)
my-job:
  image: ghcr.io/inful/repo-mr-file:latest-debug
  script:
    - repo-mr-file --help                          # the binary
    - ls /                                          # inspect the image
    - cat /etc/os-release                           # check the base
    - env | sort | grep -i api                      # see inherited env
```

…or build from source:

```bash
go build -trimpath -ldflags="-s -w" -o dist/repo-mr-file ./cmd/repo-mr-file
```

## Supported platforms

| Platform | API root shape | Auth header | Branch creation | Stale-branch conflict |
|---|---|---|---|---|
| GitLab   | `https://host/api/v4` | `PRIVATE-TOKEN: <token>` | implicit via `start_branch` on POST file | 409 |
| GitHub   | `https://api.github.com` (no API version in path) | `Authorization: Bearer <token>` | explicit `POST /git/refs` (bundled by the bundler) | 422 |
| Gitea    | `https://host/api/v1` | `Authorization: token <token>` | explicit `POST /branches` (bundled by the bundler) | 422 |
| Forgejo  | same as Gitea (API-compatible fork) | same | same | same |

Pick the platform with `--platform=gitlab\|github\|gitea\|forgejo`. The
default is `gitlab`. `--api-base` / `--api-token` work for all four.

## Usage

```bash
repo-mr-file \
  --label=v1.2.3 \
  --platform=github \
  --github-user=octocat \
  --repo=octocat/hello \
  --target-path=docs/ips.txt \
  --source-path=./local/ips.txt
```

Or for GitLab (the default platform):

```bash
repo-mr-file \
  --label=v1.2.3 \
  --repo=some-group/some-project \
  --target-path=path/inside/repo/some-file.txt \
  --source-path=/local/path/to/source-file.txt
```

…or rely on env vars for the values that have them:

**Required flags** (the first five rows are mandatory):

| Flag              | Env var        | Default                                       |
|-------------------|----------------|-----------------------------------------------|
| `--label`         | —              | (required; any identifier — release version, date, change name) |
| `--repo`          | —              | (required)                                    |
| `--target-path`   | —              | (required)                                    |
| `--source-path`   | —              | (required)                                    |
| `--api-token`     | `API_TOKEN`    | (required)                                    |
| `--platform`      | —              | `gitlab` (`gitlab` \| `github` \| `gitea` \| `forgejo`) |
| `--github-user`   | —              | (required when `--platform=github`; the GitHub handle that owns the token, used to format PR `head` as `user:branch`) |
| `--api-base`      | `API_BASE`     | `https://gitlab.com/api/v4` (GitLab SaaS default; for GitHub use `https://api.github.com`, for Gitea/Forgejo use `https://host/api/v1`, for self-hosted GitLab set `API_BASE` to your instance URL) |
| `--api-url`       | `API_URL`      | derived from `--api-base` (strips `/api/v4` / `/api/v3` / `/api/v1`) |
| `--target-branch` | `TARGET_BRANCH`| project default branch                        |
| `--branch-name`   | —              | `update-${LABEL}`                             |
| `--commit-message`| —              | `Update ${TARGET_PATH} to ${LABEL}`           |
| `--mr-title`      | —              | derived from `--commit-message`               |
| `--mr-description`| —              | `Updates ${TARGET_PATH} to ${LABEL}.`         |
| `--retries`       | —              | `3` (additional attempts; total = `--retries+1`) |
| `--retry-backoff` | —              | `500ms` (exponential with jitter)             |
| `--log-format`    | —              | `text` (`text` \| `json`)                     |
| `--verbose`       | —              | `false`                                       |
| `--dry-run`       | —              | `false`                                       |

## Publishing multiple files

The tool handles one file per invocation. To publish several files into
one merge/pull request, invoke the binary once per file with the same
`--label`, `--branch-name`, and `--target-branch`. The first call POSTs
the file and creates the MR/PR; subsequent calls PUT their files onto
the same branch and reuse the existing MR/PR.

```bash
files=(configs/a.yaml configs/b.yaml configs/c.yaml)
for f in "${files[@]}"; do
  repo-mr-file \
    --label=v1.2.3 \
    --platform=gitlab \
    --repo=foo/bar \
    --branch-name=update-v1.2.3 \
    --target-branch=main \
    --target-path="$f" \
    --source-path="./local/$f"
done
```

Notes:

- **Commit granularity**: each invocation produces its own commit on
  the branch (`Update configs/a.yaml to v1.2.3`,
  `Update configs/b.yaml to v1.2.3`, ...). The MR/PR
  description is re-templated per invocation; pass `--mr-description`
  to one invocation to pin a single description for the whole MR/PR.
- **Fail-fast**: if any invocation exits non-zero, the loop stops.
  Files already uploaded stay on the branch; the operator fixes the
  cause and re-runs the loop. Later invocations are idempotent —
  the tool compares the source against the current file and skips the
  upload when they match.
- **Idempotency**: re-running a file that already matches its target
  is a no-op (no commit, no MR/PR change).
- **Ordering**: invoke files in dependency order; commits land in
  invocation order.


## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | Success (file written + MR/PR created, or no-op)    |
| `2`  | Configuration error (missing flag, token, source) |
| `3`  | Auth / permission error (401, 403)                 |
| `4`  | Not found (404 on project)                         |
| `5`  | Conflict (stale file branch head)                  |
| `6`  | Transient failure exhausted (5xx / 429)            |
| `7`  | Unexpected internal error                          |

Note: stale-branch conflicts map to exit 5 on all platforms. GitLab
returns 409; GitHub and Gitea/Forgejo return 422 — both are normalized
to `KindConflict`.

## Design rationale

The tool mirrors what a careful human operator would do by hand when
asked to "publish this file to that repo and make sure there's a MR
open":

1. Verify the source file exists and is readable.
2. Resolve the target project (ID + default branch).
3. Decide which branch to push from: a per-update branch if it exists,
   otherwise the target branch.
4. Look up an existing open MR/PR for that source/target pair.
5. Read the current target file. If it already matches the source,
   skip the write but still ensure the MR/PR exists. If it's missing,
   POST the new file. If it differs, PUT with the platform's blob
   SHA so stale-branch conflicts surface as a typed conflict error.
6. Create the MR/PR if none exists. On **GitHub and Gitea/Forgejo** a
   422 response (concurrent MR was created) re-lists and reuses; on
   **GitLab** the equivalent condition is a 409 response, which the
   platforms layer maps to `KindConflict` (no automatic re-list).

The defaults are deliberately generic so the tool doesn't bake in any
one project's conventions. Override `--branch-name`, `--commit-message`,
`--mr-title`, or `--mr-description` to taste.

Compared to the original hand-written `curl` + `grep` script this
replaces:

- Typo-resistant: each flag binds independently; no positional
  arguments.
- Typed JSON parsing via official platform clients (or hand-rolled
  `net/http` for Gitea/Forgejo).
- 5xx / 429 responses retry with exponential backoff. When the server
  supplies a `Retry-After` header (RFC 7231 §7.1.3, supporting both
  delta-seconds and HTTP-date forms), that value is honored instead
  of the backoff, capped at 60 seconds.
- Stale-branch conflicts surface as a typed conflict (exit 5) on
  every platform.
- Distinct exit codes so CI can branch on the failure class.

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
go build -trimpath -ldflags="-s -w" -o dist/repo-mr-file ./cmd/repo-mr-file  # build static binary
go mod tidy                                        # tidy module dependencies
```

The `lefthook` pre-commit hook runs `golangci-lint run` and
`go test -race -count=1 ./...` on staged changes.

### TDD workflow

Every code change follows: red (failing test) → green (impl) → refactor →
tests green → lint clean → conventional commit. Commit messages follow
the [Conventional Commits](https://www.conventionalcommits.org/)
specification (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `chore:`,
`build:`, `ci:`).