---
name: sync-upstream
description: Sync the fork's working branch with googleapis/mcp-toolbox upstream — fetch upstream/main, merge it into feat/duckdb-quack, resolve the recurring go.mod/go.sum conflict via go mod tidy, then build and run the unit tests. Use when asked to "sync upstream", "pull in upstream changes", "merge upstream", or "keep the branch up to date".
---

# Sync upstream into the DuckDB/Quack fork

This fork (`github.com/mitja/mcp-toolbox-duckdb`) tracks the upstream project
`googleapis/mcp-toolbox`. The DuckDB/Quack adapter work lives on the
`feat/duckdb-quack` branch, which is treated as the fork's main branch. Keep it
current by merging upstream `main` in — never rebase the shared feature branch,
and never commit directly to local `main`.

Follow these steps in order. Stop and ask the user if anything other than the
known `go.mod`/`go.sum` conflict appears.

## 0. Sync with origin first (avoid the double upstream-merge)

The remote `origin/feat/duckdb-quack` also receives upstream changes by a second
path — GitHub's "Sync fork" button (or work from another machine) merges
`origin/main` into it. If you merge `upstream/main` locally without first pulling
`origin`, the two upstream-merge paths diverge and the later push is rejected as
non-fast-forward. So always integrate origin **before** touching upstream:

```bash
git checkout feat/duckdb-quack
git fetch origin
git log --oneline feat/duckdb-quack..origin/feat/duckdb-quack   # what origin has that we don't
git merge origin/feat/duckdb-quack                              # fast-forward or clean merge
```

If this pulled in upstream merges that origin already did, the later
`upstream/main` merge (step 3) may be a no-op or much smaller — that's expected.
Resolve any `go.mod`/`go.sum` conflict here the same way as step 4.

## 1. Make sure the upstream remote exists, then fetch

```bash
git remote get-url upstream >/dev/null 2>&1 || \
  git remote add upstream https://github.com/googleapis/mcp-toolbox.git
git fetch upstream
```

The first fetch downloads a lot of history and can take a while — run it in the
background and wait for it (poll `git rev-parse --verify upstream/main`).

## 2. Confirm a clean tree and check divergence

```bash
git status -s                                   # must be clean before merging
git log --oneline feat/duckdb-quack..upstream/main   # what's new upstream
git rev-list --count feat/duckdb-quack..upstream/main
```

If there is nothing new, report that the branch is already up to date and stop.
If the working tree is dirty, stop and ask the user how to proceed.

## 3. Merge upstream/main into the working branch

Make sure you are on `feat/duckdb-quack` first.

```bash
git checkout feat/duckdb-quack
git merge upstream/main
```

## 4. Resolve conflicts

The **recurring, expected** conflict is in `go.mod` (and sometimes `go.sum`):
upstream and the fork pin different versions of transitive deps that the DuckDB
driver pulls in (e.g. `github.com/google/flatbuffers`, `github.com/google/pprof`,
Arrow). Resolution:

1. In `go.mod`, keep the fork (HEAD) side of the conflict — it carries the
   newer versions the DuckDB/Arrow stack needs (`google/pprof` only exists on
   our side). Remove the `<<<<<<<`, `=======`, `>>>>>>>` markers.
2. Run `go mod tidy` to reconcile `go.mod` and `go.sum` deterministically.
3. Confirm no conflict markers remain:
   `grep -n '<<<<<<<\|>>>>>>>\|=======' go.mod go.sum` (expect none).

If conflicts appear in **any other file**, do not guess — stop and ask the user.

## 5. Build before committing the merge

```bash
go build ./...
```

Must exit 0. If it fails, the upstream refactor likely touched an interface the
DuckDB adapter implements (e.g. the `BaseTool`/`ConfigBase` embedding refactor,
or `Source.ToConfig`) — fix the adapter to match before finishing the merge.

## 6. Finalize the merge commit

```bash
git add go.mod go.sum
git diff --name-only --diff-filter=U   # must be empty (no unmerged files)
git commit --no-edit                   # keep the default merge message
```

Do **not** add a `Co-Authored-By: Claude` trailer (fork convention — see
`CLAUDE.local.md`).

## 7. Run the tests

Always run, in this order:

```bash
# DuckDB/Quack packages first (fast signal)
go test -race ./internal/sources/duckdbquack/... ./internal/tools/duckdb/...

# Full unit suite — catches breakage from upstream refactors
go test ./cmd/... ./internal/...
```

Both must pass with no `FAIL`/`panic`. The integration tests under
`tests/duckdbquack/` need a running Quack server, so do **not** run them as part
of a routine sync — mention they were skipped.

## 8. Report

Summarize: number of upstream commits merged, how the `go.mod` conflict was
resolved, build status, and test results. Do not push unless the user asks.
