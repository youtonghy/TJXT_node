# Agent rules

## One product, two repositories

TONG-ladder and TJXT_node are one product and are developed together:

- **TONG-ladder** is the management side: the Rust backend `v2board-rs/` and the admin, user, site and reseller panels. Local checkout: `/Volumes/BIGDISK/github/TONG-ladder`, remote `git@github.com:youtonghy/TONG-ladder.git`.
- **TJXT_node** (this repository) is the node side: the Go node agent that runs Xray (built against the `youtonghy/xray-core` fork, see the `replace` lines in `go.mod`) and talks to the panel. Local checkout: `/Users/youtonghy/github/Project/TJXT_node`, remote `git@github.com:youtonghy/TJXT_node.git`.

The contract between them is the node-facing API: `/api/v2/server/config` and `/api/v1/server/UniProxy/*` (user list, push, alive, alivelist), the `server_key` pairing and node credentials, and the inbound settings the panel produces (`v2board-rs/server/src/node_inbound.rs`, `node_secrets.rs`, `routes/` in TONG-ladder) that the node consumes (`api/panel/`, `node/`, `core/`).

- When a change touches that contract — routes, request or response fields, auth, pairing, protocol or transport settings, traffic and online-IP reporting — check the other repository and change both sides in the same task. Do not leave one side expecting fields or behaviour the other does not provide.
- Keep both sides compatible during rollout: panels and nodes are upgraded independently, so a new field must be optional or have a safe default on the side that reads it, unless the task explicitly drops support for older versions.
- Commit and push each repository separately, following that repository's `AGENTS.md`; mention the matching commit in the other repository in the final response.
- Keep the two `AGENTS.md` files in sync: when the shared rules here change, apply the same change to the other repository.

## Git delivery

- Before starting, run `git fetch` and `git pull --ff-only` in each repository you will touch. If a fast-forward is not possible, do not stash, reset or rebase the user's work; start from `origin/<branch>` and say so in the final response.
- Do every complex task — anything beyond a parameter change or a few localized lines — in a dedicated Git worktree on its own branch, not in the main checkout, which may hold the user's uncommitted work. After creating the worktree, run `go mod download` before building.
- Hand the result back locally: bring the branch into the main checkout (fast-forward or cherry-pick) without modifying the user's uncommitted changes, rerun the relevant checks there, then remove the worktree with `git worktree remove --force <path>` and delete the merged branch with `git branch -d`.
- After completing each task, create one or more Git commits for the changes made in that task.
- Group commits by change category or repository responsibility when the task includes unrelated changes.
- Run the relevant validation commands before committing whenever practical, and mention any validation that could not be run. For this repository (matching `.github/workflows/ci.yml`, with `GOEXPERIMENT=jsonv2`): `gofmt -l` on the tracked Go files must print nothing, `go vet ./...`, `go test -race -count=1 ./...`, and `bash -n deploy.sh` when the installer changed. When the panel contract changed, also run TONG-ladder's checks.
- Push the created commits to the current branch's upstream remote after committing.
- If committing or pushing is blocked, report the blocker explicitly and leave the working tree status clear in the final response.
- Do not include unrelated local changes in a task commit. Preserve user changes unless the user explicitly asks to modify or discard them.

## Code quality

- Follow the existing style, naming, architecture and test patterns; look for an existing helper before adding a new one.
- Make the smallest change that fully solves the problem; no patch-style workarounds.
- Handle errors explicitly; do not silently swallow them.
- Read secrets only from environment variables or the runtime config. Never commit, log, echo or hard-code them, including node keys, pairing keys and SSH credentials.
- When behaviour, commands, configuration or a public API changes, update the nearby documentation (`README.md`, `config.example.json`, `deploy/`) in the same task.
