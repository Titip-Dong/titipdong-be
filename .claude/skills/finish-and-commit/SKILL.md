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

- **Branches**: `main` is production (every push deploys), `dev` is integration. Feature branches are cut from an
  up-to-date `dev` (`git switch dev && git pull --ff-only && git switch -c feat/<module>`): `feat/notification`,
  `feat/trip`, `feat/dispute`, … Never commit directly to `dev` or `main`.
- **One commit per item of the module's "Remaining work" list.** Each commit is complete for that item (migration,
  queries, regenerated sqlc, code, tests, docs) and passes `make check` on its own. Never split one item by layer
  (a migration-only commit, then a handler commit).
- Separate commits for: pure refactors (`refactor`), docs-only changes (`docs`), tooling/config (`chore`), and fixes
  to earlier work (`fix`).
- **Feature → `dev`, no PR**: when every planned item on the branch is committed and `make check` plus the
  integration tests pass, summarize the commits and test results and **ask the user** whether to merge into `dev`.
  Only after an explicit yes in this conversation:

  ```sh
  git push origin feat/<branch>    # remote feature branch first, so it matches what is merged
  git switch dev && git pull --ff-only
  git merge --no-ff feat/<branch> -m "Merge branch 'feat/<branch>' into dev"
  make check                      # dev must stay green
  git push origin dev             # runs check.yml, no deploy
  git branch -d feat/<branch>
  ```

  If the merge conflicts or `make check` fails on `dev`, stop and report; do not push.
- **`dev` → `main` only when the user asks for a release.** Same steps with `dev` merged into `main`
  (`Merge branch 'dev'`). Pushing `main` triggers `.github/workflows/deploy-aks.yml` (build and rollout to AKS), so
  say that when asking.
- Feature branches are pushed to the remote as part of the merge flow above and never deleted there.

### Message
- Message: Conventional Commits with a module scope, imperative, lowercase, no trailing period:
  `feat(order): reserve trip capacity on create`, `fix(payment): ...`, `docs(trip): ...`,
  `refactor(platform): ...`, `chore(dev): ...`, `test(payment): ...`. Subject line only: no body/description (`git commit -m "<subject>"` once).
- **No AI attribution**: no `Co-Authored-By: Claude`, no "Generated with Claude Code", no session links. Do not pass
  `--author` or override `user.name` / `user.email`; the commit belongs to the local git identity.
- Never force-push or rewrite pushed history unless the user asked.
