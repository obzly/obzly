# Kubernetes manifests (EKS / Graviton) — HA variant

Multi-replica variant of `../`. See `ARCHITECTURE.md`'s "High availability" and "Scale
architecture" sections for the design. Use this instead of the parent directory's manifests when
you want several request-serving replicas and a separately scaled background tier rather than the
single-replica default. The Helm chart's `mode: ha` (`deploy/helm/obzly`) renders the same pods
from values.

## What's in `kustomization.yaml`

- `obzly-broker` (`broker.yaml`): the NATS JetStream ingest buffer. `SERVICE_MODE=broker`, a
  three-pod StatefulSet (one per zone, on-demand nodes only, a 50 Gi PVC each), streams
  replicated three ways, a PodDisruptionBudget so brokers roll one at a time.
- `obzly-api` (`deployment-api.yaml`): `SERVICE_MODE=api`, `replicas: 2`, `RollingUpdate`,
  `emptyDir` local disk. Serves the UI, the query API and ingest (REST, OTLP HTTP and gRPC), and
  coordinates queries. Readiness fails on SIGTERM and the pod keeps serving for
  `SHUTDOWN_DRAIN_SECONDS` while the Service deregisters it. Point an HPA at it for autoscaling.
- `obzly-worker` (`deployment-worker.yaml`): `SERVICE_MODE=worker`, `replicas: 1`, no ports,
  Service or HTTP probes. Writes ingested batches from the broker to Parquet and runs compaction,
  retention, quotas, uploads, reconciliation, alert evaluation and delivery, and the weekly
  integrity audit. Its memory limit is 3 GiB: DuckDB is capped at
  1 GB for compaction and 1 GB for its own searches (quota cutoffs, correlation).
- `obzly-querier` (`deployment-querier.yaml`): `SERVICE_MODE=querier`, `replicas: 2`, query
  executors with a 20 Gi `emptyDir` segment cache. Stateless, so they suit Spot nodes.
- `networkpolicy-querier.yaml`: only `obzly-api` pods may reach the executors' result port
  (`4319`); metrics (`9464`) stay open to scrapers. Needs a CNI that enforces NetworkPolicy.
- `service.yaml`: points only at `obzly-api`'s pods.
- Shared with `../` through `../common/`: the namespace, service account, configmap and Valkey.

**Ingest.** `obzly-api` acknowledges a batch only after the brokers hold it (`INGEST_BUFFER=
jetstream`), and `obzly-worker` writes it to Parquet, so losing an `api` pod, a worker or one
broker loses no acknowledged data. Rows are searchable while they wait in the broker. When the
buffer is full, ingest answers `429` / `RESOURCE_EXHAUSTED` and OTel Collectors retry. Each
signal's buffer holds up to 10 GiB (`INGEST_STREAM_MAX_BYTES`); keep the three of them inside the
broker PVC, sized for about an hour of ingest. `obzly-api` and `obzly-worker` create the streams
at startup and restart until the brokers answer.

**Queries.** Each `obzly-api` replica splits a query into fragments for the executors, found
through the brokers within seconds. Executors read segments from object storage into their own
cache (a segment keeps going to the same executor, so caches stay warm), and the `api` replica
merges the results. A failed fragment is retried on another executor, then in the `api` pod; with
no executors running, `api` pods run queries themselves.

## Before applying

Same prerequisites as `../README.md` (IRSA role, S3 bucket, external Postgres), plus:

- **`NATS_TOKEN` and `QUERIER_TOKEN` in `../secret.yaml`**: replace both `<RANDOM_SECRET>`
  placeholders with different long random strings (`openssl rand -hex 32`).
- **On-demand nodes for the brokers**: `broker.yaml` selects `eks.amazonaws.com/capacityType:
  ON_DEMAND`; change it to your cluster's on-demand label (e.g. `karpenter.sh/capacity-type:
  on-demand`). Brokers must never run on Spot. The PVCs use the cluster's default storage class.

- **`OBJECT_STORAGE_URL` (`../common/configmap.yaml`) is REQUIRED here, not optional.** Each pod's
  local disk is its own; object storage is how one replica reads segments another wrote.

`QUERY_MAX_SCAN_GB` (default 100) limits how much stored data one user query may read; set it in
`../common/configmap.yaml` if your largest legitimate queries need more. See the main README's
"Enterprise-grade logging and monitoring" section and `deploy/observability/` for the metrics,
dashboard and alerts.

## Applying

```bash
# Everything except secrets:
kubectl apply -k .

# Secrets, same as ../README.md:
kubectl apply -f ../secret.yaml
```

Don't apply this directory's kustomization AND the parent's against the same namespace at the
same time - pick one. Migrating from the single-replica `../` deployment: apply this
kustomization (it creates the brokers, `obzly-api`, `obzly-worker` and `obzly-querier` alongside the still-running `obzly`
Deployment), confirm `obzly-api`'s pods are healthy and serving traffic, then
`kubectl delete deployment obzly service obzly pvc obzly-data -n obzly`.

## Enroll as a Site

Same as `../README.md`'s "Enroll as a Site" section - the multi-site env vars live in the shared
`../common/configmap.yaml`, so there's nothing HA-specific to change. Only `obzly-api` serves
`/api/v1/auth/site/exchange` and the proxied query surface, so the ingress restriction in that
section only needs to cover `obzly-api`'s Service. Brokers and executors are never exposed outside
the Site.

## Migrating from an older `ha/` install

Earlier versions of these manifests ran without the broker: each `obzly-api` replica wrote its own
segments. Apply this version as a normal rolling update; the brokers start first, the `api` and
`worker` pods restart until the brokers answer, and segments already written stay readable.

## Scaling further

- `obzly-api`: `kubectl scale deployment obzly-api -n obzly --replicas=N`, or an HPA. Every
  replica is identical.
- `obzly-worker`: more replicas add ingest and compaction throughput (each signal's 64 shards are
  shared out by lease); a lost worker's shards move to the others when its lease expires.
- `obzly-querier`: add replicas for query throughput; adding or removing one moves only about 1/N
  of segments to a new executor, so caches mostly stay warm.
- `obzly-broker`: stays at three (one per zone). Size its PVCs and `INGEST_STREAM_MAX_BYTES` for
  your ingest rate.
