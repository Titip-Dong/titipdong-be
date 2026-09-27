# TITIP DONG backend

Initial Go Fiber API foundation for the TITIP DONG prototype. No use-case
routes or integrations have been implemented yet.

## Current deployment

The smoke API runs on a two-node AKS cluster through two replicas in the
`titipdong` namespace. The internal `ClusterIP` Service is intentionally not
public; use `kubectl port-forward` to reach it locally. The image is stored in
Azure Container Registry and GitHub Actions runs separate test, build/push,
and AKS-deploy jobs. See [`deploy/k8s/README.md`](deploy/k8s/README.md) for
manual deployment and smoke-check commands.

## Run

```sh
# One-time local setup:
cp .env.example .env.local
# Then set DATABASE_URL and JWT_SECRET in .env.local.

go run ./cmd/api
```

The API listens on `:8080`.

`DATABASE_URL` and `JWT_SECRET` are required by the PostgreSQL/JWT Auth & User
API. Keep real values only in the ignored `.env.local` file.

## Smoke check

`GET /health` returns `200 OK` with:

```json
{"status":"ok"}
```

## API contract handoff

`GET /openapi.json` serves the current API contract, including implemented
`/health`, registration, login, and authenticated-profile routes. See
[`docs/FIRST_MILESTONE.md`](docs/FIRST_MILESTONE.md) for the prototype's
security and deferred-work boundaries.
