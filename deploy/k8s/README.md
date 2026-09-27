# Deploy the API to AKS

These manifests create two API pods in the `titipdong` namespace and an internal `ClusterIP` service. There is no Ingress or public IP yet. The active image registry is `titipdongacr2026.azurecr.io`.

> The Deployment does not yet pass `DATABASE_URL` and `JWT_SECRET`, which the API requires. See the infrastructure steps in [`docs/PLAN.md` section 6](../../docs/PLAN.md#6-delivery-pipeline-and-infrastructure).

## 1. Confirm the image registry

The checked-in Deployment already uses `titipdongacr2026.azurecr.io`. Confirm the login server when deploying from a new terminal:

```bash
az acr show \
  --name "$TITIP_ACR" \
  --query loginServer \
  --output tsv
```

The command should return:

```text
titipdongacr2026.azurecr.io
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

## 3. Deploy and verify

```bash
kubectl apply -f titipdong-be/deploy/k8s/namespace.yaml
kubectl wait \
  --for=jsonpath='{.status.phase}'=Active \
  namespace/titipdong \
  --timeout=60s

kubectl apply -f titipdong-be/deploy/k8s/service.yaml
kubectl apply -f titipdong-be/deploy/k8s/deployment.yaml
kubectl rollout status deployment/titipdong-api --namespace titipdong
kubectl get pods --namespace titipdong --output wide
kubectl port-forward service/titipdong-api 8080:8080 --namespace titipdong
```

In a second terminal:

```bash
curl http://localhost:8080/health
```

Expected response:

```json
{"status":"ok"}
```

The two API pods must be on different nodes. The Deployment uses required pod anti-affinity to enforce that constraint. If either node is unavailable, the second pod remains pending rather than being scheduled alongside the first pod.

The Deployment uses `maxSurge: 0` and `maxUnavailable: 1` during rolling updates. With only two nodes and required anti-affinity, this lets Kubernetes remove one old pod before scheduling its replacement; the default surge behavior would have no eligible node for a third pod.
