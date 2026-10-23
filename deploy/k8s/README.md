# Deploy the API to AKS

This deployment creates two API pods in the `titipdong` namespace behind an internal Service, and Caddy as the public HTTPS entry point at `https://titipdong-api.indonesiacentral.cloudapp.azure.com`. Everything lives in resource group `rg-titipdong` (Indonesia Central): AKS `aks-titipdong` (one `Standard_B2als_v2` node), registry `titipdongacr20261023.azurecr.io`, PostgreSQL `titipdongpg20261023`, and storage account `titipdongst20261023`.

## 1. Confirm the image registry

The checked-in Deployment already uses `titipdongacr20261023.azurecr.io`. Confirm the login server when deploying from a new terminal:

```bash
az acr show \
  --name "$TITIP_ACR" \
  --query loginServer \
  --output tsv
```

The command should return:

```text
titipdongacr20261023.azurecr.io
```

## 2. Build and push

Run these commands from the workspace root. Buildx is required because AKS runs AMD64 nodes while local development may run on Apple Silicon.

```bash
az acr login --name "$TITIP_ACR"

export TITIP_ACR_LOGIN_SERVER="$(az acr show --name "$TITIP_ACR" --query loginServer --output tsv)"

docker buildx build \
  --platform linux/amd64 \
  --tag "$TITIP_ACR_LOGIN_SERVER/titipdong-api:manual" \
  --push \
  ./titipdong-be
```

## Apple Silicon / image architecture

An `exec format error` in the pod log means the image CPU architecture does not match the AKS node. Check the cluster architecture:

```bash
kubectl get nodes \
  --output=jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.nodeInfo.architecture}{"\n"}{end}'
```

For AMD64 AKS nodes, build and push an AMD64 image. This is required when building from an Apple Silicon Mac. Rebuild with the same tag and restart the Deployment:

```bash
docker buildx build \
  --platform linux/amd64 \
  --tag "$TITIP_ACR_LOGIN_SERVER/titipdong-api:manual" \
  --push \
  ./titipdong-be

kubectl rollout restart deployment/titipdong-api --namespace titipdong
kubectl rollout status deployment/titipdong-api --namespace titipdong
```

The Dockerfile uses Docker Buildx target-platform arguments so that this command compiles the Go binary for `linux/amd64`.

## 3. Secrets and configuration

The Deployment reads its configuration from one Secret and one optional ConfigMap in the `titipdong` namespace.

| Source                         | Key                               | Required | Notes                                                       |
| ------------------------------ | --------------------------------- | -------- | ----------------------------------------------------------- |
| Secret `titipdong-api-secrets` | `DATABASE_URL`                    | yes      | Azure PostgreSQL, `sslmode=require&pool_max_conns=10` (see below) |
| Secret `titipdong-api-secrets` | `JWT_SECRET`                      | yes      | At least 32 characters (`openssl rand -base64 32`)          |
| Secret `titipdong-api-secrets` | `AZURE_STORAGE_CONNECTION_STRING` | no       | Storage account connection string with an account key; uploads return 503 without it |
| ConfigMap `titipdong-api-config` | `CORS_ORIGINS`                  | no       | Comma-separated frontend origins; defaults to `http://localhost:5173` |

`PAYMENT_GATEWAY` is fixed to `fake` in the manifest, and `COOKIE_SECURE` keeps its secure default. On start, the API
creates the private `uploads` container and sets the storage account's CORS rules to `CORS_ORIGINS`.

```bash
kubectl create secret generic titipdong-api-secrets --namespace titipdong \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=JWT_SECRET="$JWT_SECRET" \
  --from-literal=AZURE_STORAGE_CONNECTION_STRING="$AZURE_STORAGE_CONNECTION_STRING" \
  --dry-run=client --output yaml | kubectl apply -f -

kubectl create configmap titipdong-api-config --namespace titipdong \
  --from-literal=CORS_ORIGINS="https://<frontend-domain>" \
  --dry-run=client --output yaml | kubectl apply -f -
```

Changing either one requires `kubectl rollout restart deployment/titipdong-api --namespace titipdong`.

Keep `pool_max_conns=10` in `DATABASE_URL`. Each background job holds one pool connection for its advisory lock while
its body takes another, and pgx defaults to `max(4, CPUs)` connections: on a 2-vCPU node the four jobs can take all
four and deadlock the pod (readiness hangs, rollout times out).

## 4. Deploy and verify

```bash
kubectl apply -f titipdong-be/deploy/k8s/namespace.yaml
kubectl wait \
  --for=jsonpath='{.status.phase}'=Active \
  namespace/titipdong \
  --timeout=60s

kubectl apply -f titipdong-be/deploy/k8s/service.yaml
kubectl apply -f titipdong-be/deploy/k8s/deployment.yaml
kubectl apply -f titipdong-be/deploy/k8s/caddy.yaml
kubectl rollout status deployment/titipdong-api --namespace titipdong
kubectl get pods --namespace titipdong --output wide
kubectl get service caddy --namespace titipdong --watch
```

The `caddy` Service's annotation names its public IP `titipdong-api.indonesiacentral.cloudapp.azure.com`; Caddy
requests a Let's Encrypt certificate on first start and keeps it on the `caddy-data` disk. Then:

```bash
curl https://titipdong-api.indonesiacentral.cloudapp.azure.com/health
curl https://titipdong-api.indonesiacentral.cloudapp.azure.com/health/ready   # 503 while the database is unreachable
```

Expected response:

```json
{"status":"ok"}
```

## 5. Create the first admin

Admins exist only through the CLI (there is no admin sign-up). Run it in a live pod: it reuses the pod's
`DATABASE_URL`, and reading the password from stdin keeps it out of shell history. Never run `seed-demo` here; its
accounts share the password `demo12345`.

```bash
kubectl exec -i -n titipdong deploy/titipdong-api -- sh -c \
  'read -r ADMIN_PASSWORD; export ADMIN_PASSWORD; /titipdong-api create-admin -name "Admin" -email admin@<domain>'
```

Type the password (8–128 characters) and press Enter. Rerunning with an email already registered is refused.

## 6. Frontend and API domains

The refresh token is an `HttpOnly; SameSite=Lax` cookie on `/auth`. Browsers send it only when the frontend and the
API are the same site, that is, under one registrable domain (for example `app.<domain>` and `api.<domain>`). With
the frontend on `*.azurestaticapps.net` and the API on another domain, `POST /auth/refresh` gets no cookie and users
are signed out on every reload and every 15 minutes. Put both behind custom domains of one parent domain, serve the
API through the HTTPS Ingress, and set `CORS_ORIGINS` to the frontend origin before uploads are used: on start the
API also copies `CORS_ORIGINS` into the storage account's CORS rules, without which browsers block photo uploads.

For controlled monitoring, `/metrics` exposes Prometheus-format application and
Go runtime metrics. It is not a readiness or liveness endpoint. The local
Prometheus/Grafana setup and two-pod port-forward procedure are documented in
`titipdong-docs/MONITORING_RUNBOOK.md`.

The Deployment prefers to place the two API pods on different nodes (preferred pod anti-affinity), so they spread
when the cluster has several nodes and still both run on the current single node. It uses `maxSurge: 0` and
`maxUnavailable: 1` during rolling updates, replacing one pod at a time.

The frontend is served from the storage account's static website (`https://titipdongst20261023.z45.web.core.windows.net`);
Static Web Apps is not offered in the regions this subscription allows. The frontend and API are different sites, so
the refresh cookie still needs one custom parent domain (above).
