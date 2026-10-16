---
name: finish-and-commit
description: Definition of done for a titipdong-be change - verification commands, docs sync, and commit message format. Use before saying a task is done, before committing, and before pushing.
---

# Finish and commit

## 1. Verify (run them, read the output)

```sh
make fmt
make check                                   # fmt-check, vet, golangci-lint, sqlc drift, unit tests
make db-up                                   # local PostgreSQL (+ API) for integration tests
TEST_DATABASE_URL="$DATABASE_URL" go test ./... -count=1
```

- `DATABASE_URL` comes from `.env` (`set -a; . ./.env; set +a` to load it into the shell).
- If an endpoint changed, start the API and hit it once with curl (`make seed` gives demo accounts, password
  `demo12345`) to confirm status code and body shape, or check `/openapi.json` for the operation.
- Report results as they are. If something fails or was skipped (no Docker, no database), say exactly that with the
  error output. Never write "tests pass" without having run them in this session.

## 2. Docs in sync (same commit as the code)

- `docs/features/<module>.md`: endpoint table `Status` → `Done`, move items from "Remaining" to "Done", fix any
  request/response field, status code, or rule that ended up different.
- `docs/DATABASE.md`: table markers and DDL match the migration.
- `docs/PLAN.md` §1 status table and `docs/features/README.md` module status when a module's state changes.
- Never document behaviour that is not implemented as done.

## 3. Commit

### Granularity

- **Branch per module or phase**, from an up-to-date `main`: `feat/platform`, `feat/notification`, `feat/trip`,
  `feat/dispute`, … Never commit directly to `main` unless asked.
- **One commit per item of the module's "Remaining work" list.** Each commit is complete for that item (migration,
  queries, regenerated sqlc, code, tests, docs) and passes `make check` on its own. Never split one item by layer
  (a migration-only commit, then a handler commit).
- Separate commits for: pure refactors (`refactor`), docs-only changes (`docs`), tooling/config (`chore`), and fixes
  to earlier work (`fix`).
- **Merge per module or phase, no PR**: when every planned item on the branch is committed and `make check` plus the
  integration tests pass, summarize the commits and test results and **ask the user** whether to merge into `main`.
  Only after an explicit yes in this conversation:

  ```sh
  git switch main && git pull --ff-only
  git merge --no-ff feat/<branch> -m "Merge branch 'feat/<branch>'"
  make check                      # main must stay green
  git push origin main
  git branch -d feat/<branch>
  ```

  Pushing `main` triggers `.github/workflows/deploy-aks.yml` (build and rollout to AKS), so say that when asking. If
  the merge conflicts or `make check` fails on `main`, stop and report; do not push.
- Feature branches are not pushed on their own unless the user asks.

### Message
- Message: Conventional Commits with a module scope, imperative, lowercase, no trailing period:
  `feat(order): reserve trip capacity on create`, `fix(payment): ...`, `docs(trip): ...`,
  `refactor(platform): ...`, `chore(dev): ...`, `test(payment): ...`. Optional body explains why.
- **No AI attribution**: no `Co-Authored-By: Claude`, no "Generated with Claude Code", no session links. Do not pass
  `--author` or override `user.name` / `user.email`; the commit belongs to the local git identity.
- Never force-push or rewrite pushed history unless the user asked.
