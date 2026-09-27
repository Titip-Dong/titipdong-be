# First backend implementation milestone

## Implemented state

`GET /health`, `GET /openapi.json`, `POST /auth/register`, `POST /auth/login`,
and `GET /me` are implemented. The app applies the local `users` schema on
startup. Passwords are Argon2id hashes and access JWTs expire after 15 minutes.

## Chosen prototype contract

These details were not fixed by `titipdong-docs/PLAN.md` and were chosen for
the prototype implementation.

- `POST /auth/register` accepts `name`, `email`, `password`, and `role`.
  Self-registration permits only `buyer` and `jastiper`; an `admin` account is
  provisioned outside this public endpoint.
- `POST /auth/login` returns an access JWT and the safe user profile.
- `GET /me` requires `Authorization: Bearer <access-token>` and returns the
  safe user profile.
- The access token lifetime is 15 minutes, matching the architecture plan.
  Refresh token storage, an HttpOnly refresh cookie, and rotation are deferred.
- Passwords will be stored only as Argon2id hashes. API responses, logs, and
  OpenAPI examples must never contain passwords, JWTs, or password hashes.
- Validation and authentication failures will use `application/problem+json`
  with RFC 9457-style `type`, `title`, `status`, `detail`, and `instance`.

## Deferred work

The following is intentionally deferred:

1. Configure `DATABASE_URL` and `JWT_SECRET` as AKS secrets and GitHub Actions
   deployment inputs. Neither belongs in the repository or frontend variables.
2. Replace the startup schema creation with versioned Goose SQL migrations and
   add PostgreSQL integration tests in CI.
3. Add refresh-token storage/rotation only if the frontend needs persistent
   sessions.
4. Add structured request logging and more complete validation as the API grows.

No KTP verification, real payment gateway, email provider, shipping-provider
integration, Blob feature, or unrelated business module belongs in this
milestone.
