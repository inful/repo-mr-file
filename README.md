# repo-mr-file

Adds or updates a single file in a GitLab, GitHub, Gitea, or Forgejo
repository and ensures an open merge/pull request exists. The tool is
project-agnostic — it doesn't care what's in the file, so it works
equally well for cert bundles, IP allow lists, tool versions,
configuration snippets, or any other text blob you need to publish
through a merge request (MR) or pull request (PR).

## Install

```bash
go install github.com/inful/repo-mr-file/cmd/repo-mr-file@latest
```

…or build from source:

```bash
go build -trimpath -ldflags="-s -w" -o dist/repo-mr-file ./cmd/repo-mr-file
```

## Supported platforms

| Platform | API root shape | Auth header | Branch creation | Stale-branch conflict |
|---|---|---|---|---|
| GitLab   | `https://host/api/v4` | `PRIVATE-TOKEN: <token>` | implicit via `start_branch` on POST file | 409 |
| GitHub   | `https://api.github.com` (no API version in path) | `Authorization: Bearer <token>` | implicit via `branch` on PUT file | 422 |
| Gitea    | `https://host/api/v1` | `Authorization: token <token>` | explicit `POST /branches` first | 422 |
| Forgejo  | same as Gitea (API-compatible fork) | same | same | same |

Pick the platform with `--platform=gitlab\|github\|gitea\|forgejo`. The
default is `gitlab`. `--api-base` / `--api-token` work for all four.

## Usage

```bash
repo-mr-file \
  --tag=v1.2.3 \
  --repo=some-group/some-project \
  --target-path=path/inside/repo/some-file.txt \
  --source-path=/local/path/to/source-file.txt
```

…or rely on env vars for the values that have them:

| Flag              | Env var        | Default                                       |
|-------------------|----------------|-----------------------------------------------|
| `--tag`           | —              | (required)                                    |
| `--repo`          | —              | (required)                                    |
| `--target-path`   | —              | (required)                                    |
| `--source-path`   | —              | (required)                                    |
| `--platform`      | —              | `gitlab` (`gitlab` \| `github` \| `gitea` \| `forgejo`) |
| `--api-base`      | `API_BASE`     | `https://gitlab.mgmlab.net/api/v4`            |
| `--api-url`       | `API_URL`      | derived from `--api-base` (strips `/api/v4` / `/api/v3` / `/api/v1`) |
| `--api-token`     | `API_TOKEN`    | (required)                                    |
| `--target-branch` | `TARGET_BRANCH`| project default branch                        |
| `--branch-name`   | —              | `update-${TAG}`                               |
| `--commit-message`| —              | `Update ${TARGET_PATH} to release ${TAG}`     |
| `--mr-title`      | —              | derived from `--commit-message`               |
| `--mr-description`| —              | `Updates ${TARGET_PATH} from release ${TAG}.` |
| `--retries`       | —              | `3` (additional attempts; total = `--retries+1`) |
| `--retry-backoff` | —              | `500ms` (exponential with jitter)             |
| `--log-format`    | —              | `text` (`text` \| `json`)                     |
| `--verbose`       | —              | `false`                                       |
| `--dry-run`       | —              | `false`                                       |

## Publishing multiple files

The tool handles one file per invocation. To publish several files into
one merge/pull request, invoke the binary once per file with the same
`--tag`, `--branch-name`, and `--target-branch`. The first call POSTs
the file and creates the MR/PR; subsequent calls PUT their files onto
the same branch and reuse the existing MR/PR.

```bash
files=(configs/a.yaml configs/b.yaml configs/c.yaml)
for f in "${files[@]}"; do
  repo-mr-file \
    --tag=v1.2.3 \
    --repo=foo/bar \
    --branch-name=update-v1.2.3 \
    --target-branch=main \
    --target-path="$f" \
    --source-path="./local/$f"
done
```

Notes:

- **Commit granularity**: each invocation produces its own commit on
  the branch (`Update configs/a.yaml to release v1.2.3`,
  `Update configs/b.yaml to release v1.2.3`, ...). The MR/PR
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
4. Look up an existing open MR/PR for that source/target pair and
   reuse it on retries.
5. Read the current target file. If it already matches the source,
   skip the write but still ensure the MR/PR exists. If it's missing,
   POST the new file. If it differs, PUT with the platform's blob
   SHA so stale-branch conflicts surface as a typed conflict error.
6. Create the MR/PR if none exists; if the create fails with 422
   (a concurrent run beat us to it), re-list and reuse.

The defaults are deliberately generic so the tool doesn't bake in any
one project's conventions. Override `--branch-name`, `--commit-message`,
`--mr-title`, or `--mr-description` to taste.

Compared to the original hand-written `curl` + `grep` script this
replaces:

- Typo-resistant: each flag binds independently; no positional
  arguments.
- Typed JSON parsing via official platform clients (or hand-rolled
  `net/http` for Gitea/Forgejo).
- 5xx / 429 responses retry with exponential backoff; `Retry-After`
  honored.
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