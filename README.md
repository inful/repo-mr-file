# gitlab-mr-file

Adds or updates a single file in a GitLab repository and ensures an open
merge request exists. The tool is project-agnostic — it doesn't care
what's in the file, so it works equally well for cert bundles, IP allow
lists, tool versions, configuration snippets, or any other text blob you
need to publish through an MR.

## Install

```bash
go install github.com/inful/gitlab-mr-file/cmd/gitlab-mr-file@latest
```

…or build from source:

```bash
go build -trimpath -ldflags="-s -w" -o dist/gitlab-mr-file ./cmd/gitlab-mr-file
```

## Usage

```bash
gitlab-mr-file \
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
| `--gitlab-api`    | `GITLAB_API`   | `https://gitlab.mgmlab.net/api/v4`            |
| `--gitlab-url`    | `GITLAB_URL`   | derived from `--gitlab-api`                   |
| `--gitlab-token`  | `GITLAB_TOKEN` | (required)                                    |
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
one merge request, invoke the binary once per file with the same
`--tag`, `--branch-name`, and `--target-branch`. The first call POSTs
the file and creates the MR; subsequent calls PUT their files onto the
same branch and reuse the existing MR.

```bash
files=(configs/a.yaml configs/b.yaml configs/c.yaml)
for f in "${files[@]}"; do
  gitlab-mr-file \
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
  `Update configs/b.yaml to release v1.2.3`, ...). The MR description
  is re-templated per invocation; pass `--mr-description` to one
  invocation to pin a single description for the whole MR.
- **Fail-fast**: if any invocation exits non-zero, the loop stops.
  Files already uploaded stay on the branch; the operator fixes the
  cause and re-runs the loop. Later invocations are idempotent —
  the tool compares the source against the current file and skips the
  upload when they match.
- **Idempotency**: re-running a file that already matches its target
  is a no-op (no commit, no MR change).
- **Ordering**: invoke files in dependency order; commits land in
  invocation order.


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

## Design rationale

The tool mirrors what a careful human operator would do by hand when
asked to "publish this file to that repo and make sure there's an MR
open":

1. Verify the source file exists and is readable.
2. Resolve the target GitLab project (ID + default branch).
3. Decide which branch to push from: a per-update branch if it exists,
   otherwise the target branch.
4. Look up an existing open MR for that source/target pair and reuse it
   on retries.
5. Read the current target file. If it already matches the source,
   skip the write but still ensure the MR exists. If it's missing, POST
   the new file. If it differs, PUT with `last_commit_id` so stale-branch
   conflicts surface as 409.
6. Create the MR if none exists; if the create fails with 422 (a
   concurrent run beat us to it), re-list and reuse.

The defaults are deliberately generic so the tool doesn't bake in any
one project's conventions. Override `--branch-name`, `--commit-message`,
`--mr-title`, or `--mr-description` to taste.

Compared to the original hand-written `curl` + `grep` script this
replaces:

- Typo-resistant: each flag binds independently; no positional
  arguments.
- Typed JSON parsing via the official GitLab client.
- 5xx / 429 responses retry with exponential backoff; `Retry-After`
  honored.
- The previous always-`null` `last_commit_id` is now the real value
  read from the file API, so stale-branch conflicts surface as a clean
  409 (exit 5) instead of an opaque 400.
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
go build -trimpath -ldflags="-s -w" -o dist/gitlab-mr-file ./cmd/gitlab-mr-file  # build static binary
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