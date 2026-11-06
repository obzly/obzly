# Disaster recovery

Scenarios, what is lost, and what to do. Backups and the restore procedure are in
[backup-restore.md](backup-restore.md).

| Failure | Data at risk | Recovery |
| --- | --- | --- |
| An `api`, `worker` or `querier` pod | none | Kubernetes replaces it. Acknowledged ingest is in the brokers; a worker's shards move to the others when its lease expires. |
| One broker (of 3) | none | R3 streams keep a quorum. Replace the pod; its PVC catches up. Upgrade status shows replicas behind until it does. |
| Two brokers, or the broker quorum's disks | acknowledged batches not yet written to Parquet | The one loss case: JetStream is a buffer, not a store of record, and isn't backed up. Bring brokers back with their PVCs if possible; otherwise restore them empty (delete the PVCs), and api/worker recreate the streams. The loss is bounded by the worker lag at the time (Upgrade status, ingest buffer). |
| Postgres primary | writes since the last shipped WAL (RPO < 70 s at `archive_timeout = 60s`) | Fail over to a replica (RDS Multi-AZ, CNPG), or point-in-time restore, then [the restore order](backup-restore.md#restore-order). |
| Object storage objects (deleted, corrupted) | the segments affected | Restore object versions; `reconcile` reports what's missing; the integrity audit finds silent damage within a week. |
| A whole region | everything not replicated off-region | Restore Postgres from the cross-region backup copy, point at the replicated bucket, start the release in the new region with the same config, run the restore order. RPO is the backup copy's lag; brokers start empty. |
| A control plane | Sites keep serving their own users and data | Restore its Postgres first (identity root for every Site), then start it. Sites validate assertions against its JWKS; the key comes back with `secret_keys`. |
| `secret_keys` lost | sessions, SSO secrets, MFA seeds, channel credentials | See [key material](backup-restore.md#key-material-secret_keys): re-entry only. |

## Order for a full rebuild

1. Control plane Postgres, then the control plane.
2. Each Site: Postgres (key-material check), brokers, one worker, `reconcile`, api, queriers.
3. Verify: Upgrade status on each Site and the control plane, one SSO sign-in through the control
   plane to each Site, a search and a trace view per Site.

## Practising

Run the drill in [backup-restore.md](backup-restore.md#drill) quarterly and after any change to
backups; keep its measured RPO under 5 minutes.
