# Backup and restore

What to back up, how, and the order to restore in. Targets: **RPO 5 minutes** for the catalog and
identity data (Postgres), no loss for uploaded segments (object storage). Drill results are at the
end.

## What holds what

| Store | Holds | Backup |
| --- | --- | --- |
| Postgres | users, projects, settings, dashboards, alert rules, the segment catalog (`stream_segments`), and `secret_keys` - the root of trust | continuous WAL archiving + base backups (point-in-time recovery) |
| Object storage | every uploaded segment (Parquet) | bucket versioning (+ cross-region replication for DR) |
| JetStream (brokers) | acknowledged batches not yet written to Parquet | none: a buffer, replicated three ways across zones |
| Valkey | caches, rate limits, leases | none: rebuilt at start |
| Local disks / emptyDirs | segments not yet uploaded, caches | none: uploaded within seconds; caches refill |

A control plane is the same minus segments; its Postgres is the identity root for every Site
(users, SSO, organizations, Site grants), so it's restored first.

## Postgres: point-in-time recovery

Any tool that archives WAL continuously and takes base backups works. RPO comes from how often a
WAL segment is shipped: set `archive_timeout = 60s` so even a quiet database ships within a
minute (RPO measured below: under 70 s).

- **External Postgres (RDS / Aurora):** automated backups give PITR to any second within the
  retention window (5-minute RPO by AWS's own figure). Set retention to at least 7 days.
- **Self-managed:** pgBackRest (`archive-async=y`, `repo1-type=s3`, `repo1-cipher-type=aes-256-cbc`)
  or wal-g (`WALG_S3_PREFIX`, `WALG_LIBSODIUM_KEY`), with `archive_command` pointing at them,
  `archive_timeout = 60s`, a full backup weekly and differentials daily.
- **In-cluster CloudNativePG:** `spec.backup.barmanObjectStore` (WAL archiving to S3, with
  `encryption: AES256` or SSE-KMS) and a `ScheduledBackup`.

**Encrypt backups with a key held outside the database.** They contain `secret_keys` beside the
ciphertexts it protects (SSO client secrets, MFA seeds, channel credentials, session signing):
read access to the backup bucket is secret access.

## Object storage

Turn on **versioning** on the segment bucket with a noncurrent-version expiry longer than your
Postgres backup retention (e.g. 14 days), so a restored catalog can still find every object it
names - compaction deletes inputs once their merged output is cataloged, and a catalog restored to
before that merge needs them back. For regional DR add cross-region replication. Lifecycle rules
by upload tag or date prefix (retention policies) are unaffected.

## Key material (`secret_keys`)

`secret_keys` is created on first start and read with an unordered `First()`; it has no singleton
constraint. Therefore:

1. **Restores are full-database only, and finish before any obzly process (or migration Job)
   touches the database.** An app starting against an empty database mints a new key; a data-only
   restore on top of it leaves two rows and an arbitrary one in use - every session, SSO secret and
   MFA seed then fails to verify or decrypt.
2. **Acceptance check after every restore:**
   ```sql
   SELECT count(*) FROM secret_keys;   -- must be 1
   ```
   then one SSO sign-in and one MFA verification succeed, and an existing session still works.
3. **If the row is lost for good** there is no recovery of what it protected, only re-entry: SSO
   client secrets re-entered, MFA re-enrolled (`/app/main --email <user> --reset-mfa`), a new
   control-plane assertion key (Sites refresh the JWKS), everyone signs in again.

## Restore order

1. **Stop** `api`, `worker`, `querier` (scale to 0). Brokers may stay up: they keep acknowledged
   batches until a worker writes them.
2. **Restore Postgres** to the chosen point (the latest, for a lost primary). Run the key-material
   check before going on.
3. **Start one `worker`.** It runs migrations if the restore point predates the running release,
   then drains the brokers.
4. **Reconcile the catalog with object storage** (from any pod with the release's image and
   config):
   ```bash
   /app/main reconcile                                            # report only
   /app/main reconcile --apply --adopt-since 2026-10-06T11:00:00Z # repair; the restore point
   ```
   - `lost`: the catalog names a segment whose object (and local copy) is gone - restored to a
     point before a compaction whose inputs were then deleted, without bucket versioning. `--apply`
     moves the row to `lost_segments` so queries stop failing on it; restore the object version
     instead if versioning has it, then run again.
   - `reuploadable`: the object is gone but this pod still has the file; `--apply` uploads it.
   - `orphan`: an object no catalog row knows, uploaded after `--adopt-since` (the restore point):
     written after it. `--apply` adds the row from the file (row count, time range, stats).
     Without `--adopt-since` orphans are only reported.
   - `orphan-old`: an object no catalog row knows, uploaded before the restore point: data retention
     or compaction retired that was never deleted from the bucket (common for keys from before
     deletes covered objects). Never adopted - that would bring retired data back; remove it with
     a bucket lifecycle rule or by hand.
   - `orphan-overlapping`: an orphan overlapping cataloged data of its project, typically a
     compaction output whose inputs the restored catalog still lists. Never adopted automatically
     (that would double those rows); delete it if the inputs are intact, or adopt by hand.
   - `size-mismatch`: needs a person; compare with the bucket's object versions.
   Exit code 2 means findings remain. Objects newer than `--orphan-min-age` (1 h) are skipped as
   possibly in flight; `--projects` and `--streams` narrow a run.
5. **Start `api` and `querier`.** Check Settings > Upgrade status: schema up to date, no backlog
   blockers.

## Integrity audit

Every `INTEGRITY_AUDIT_HOURS` (168) a worker samples `INTEGRITY_AUDIT_PERCENT` (1%, at most
`INTEGRITY_AUDIT_MAX_SAMPLES` = 500) of uploaded segments and checks each object exists, has the
cataloged size and row count. Results: Settings > Upgrade status, `obzly_integrity_audit_*`
metrics, and the `ObzlyIntegrityAuditFailures` / `ObzlyIntegrityAuditStale` alerts. Any failure:
run `reconcile` (report only) and follow the findings above.

## Drill

Run it quarterly and after any change to backups, on copies - never against production
Postgres. The procedure:

1. A scratch Postgres with WAL archiving (`archive_mode = on`, `archive_timeout = 60s`), loaded
   with a `pg_dump` of the production database.
2. Record `SELECT count(*), md5(string_agg(t::text, ',')) FROM secret_keys t`; start an obzly
   `api` against it and sign in, keeping the session token.
3. Take a base backup, then write a heartbeat row every second (`INSERT ... RETURNING n, at`,
   keeping what each insert acknowledged).
4. Kill the primary (`docker kill`, data directory discarded).
5. Restore: base backup + `restore_command` from the archive + `recovery.signal`, recovery to
   the end of the archive. RPO = last acknowledged heartbeat - last recovered.
6. Key material: same `secret_keys` count and checksum; the old session token works against an
   obzly started on the restored database.
7. `reconcile` against the real bucket from the restored database: report, then `--apply
   --adopt-since <dump time>`, then report again.

**2026-10-06, test Site** (Postgres 18, 26 MB catalog, MinIO with 11,925 objects), three runs:

| | Result |
| --- | --- |
| RPO | 19-20 s (target 5 min) - the heartbeat rows written after the last shipped WAL segment |
| Restore time | 2-5 s (copy, WAL replay, promote) |
| `secret_keys` | 1 row before and after, same checksum |
| Session issued before the disaster | HTTP 200 before and after the restore |
| `reconcile` report | 276 cataloged rows: 15 `lost` (compacted away after the dump), 2 `orphan` (uploaded after it), 1 `orphan-overlapping`, 11,660 `orphan-old` (old-layout keys never deleted), 1 unadoptable (`metrics_series`) |
| `reconcile --apply` | 15 moved to `lost_segments`, 4 orphans adopted (uploads kept arriving), nothing old adopted |
| `reconcile` again | 265 rows (276 - 15 + 4); only `orphan-old` and the unadoptable one left |

Not covered by the drill: an SSO sign-in and an MFA verification after the restore (they need a
live identity provider and an enrolled authenticator); the session check above exercises the same
key. The first run found that adopting every orphan would resurrect the 11,660 retired objects,
which is why adoption needs `--adopt-since`.
