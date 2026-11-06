# Fake log generators

Small containers that write synthetic log lines for Nginx, MongoDB, MySQL, and
PostgreSQL - matching the exact log formats each of those data sources'
generated OTel Collector config (Obzly's **Data Sources** page) parses by
default. Useful for testing the ingestion pipeline end-to-end without
standing up the real services or driving real traffic at them.

Each generator just appends a plausible log line every second or two to the
default path its matching data source expects:

| Service    | Log path                              | Format                                   |
|------------|----------------------------------------|-------------------------------------------|
| Nginx      | `/var/log/nginx/access.log`, `error.log` | Default "combined" access log + error log |
| MongoDB    | `/var/log/mongodb/mongod.log`          | mongod's default JSON log (4.4+)          |
| MySQL      | `/var/log/mysql/error.log`             | MySQL 8.x error log                       |
| PostgreSQL | `/var/log/postgresql/postgresql.log`   | Default `log_line_prefix`                 |

## Quick start

```bash
cd tests
docker compose up -d --build
```

This starts four containers, each writing to its own named volume
(`nginx-logs`, `mongodb-logs`, `mysql-logs`, `postgresql-logs`). Nothing
ingests them yet - you still need an OTel Collector pointed at each volume.

## Wiring up an OTel Collector

1. In Obzly, go to **Data Sources** and generate a config for Nginx,
   MongoDB, MySQL, or PostgreSQL (creates a dedicated API key + collector
   config). Leave the log path fields at their defaults shown in the table
   above - they already match what these generators write.
2. Save the generated `otel-collector-config.yaml` next to the matching
   service's folder here (e.g. `tests/nginx/otel-collector-config.yaml`).
3. Add a collector service to `tests/docker-compose.yml` that mounts the
   *same* named volume these generators write to, read-only, at the path
   the config expects - for example, for Nginx:

   ```yaml
     nginx-otel-collector:
       image: otel/opentelemetry-collector-contrib:0.116.1
       container_name: obzly-test-nginx-collector
       command: ["--config=/etc/otel/config.yaml"]
       volumes:
         - ./nginx/otel-collector-config.yaml:/etc/otel/config.yaml:ro
         - nginx-logs:/var/log/nginx:ro
   ```

   Repeat for whichever of MongoDB/MySQL/PostgreSQL you're testing, mounting
   `mongodb-logs:/var/log/mongodb:ro`, `mysql-logs:/var/log/mysql:ro`, or
   `postgresql-logs:/var/log/postgresql:ro` respectively. (The MongoDB/MySQL/
   PostgreSQL data sources also scrape live metrics from the real database -
   these fake generators only cover the *log* side, so metrics will error out
   unless you also point them at a real instance; that's expected, logs will
   still flow.)

4. `docker compose up -d` again and watch logs show up in Obzly's Search
   page within a few seconds.

## Configuration

Every generator's interval and log path are overridable via environment
variables in `docker-compose.yml`:

- `LOG_INTERVAL_SECONDS` (all) - how often a new line is appended (default:
  1s for Nginx, 2s for the others).
- Nginx: `ACCESS_LOG_PATH`, `ERROR_LOG_PATH`.
- MongoDB / MySQL / PostgreSQL: `LOG_PATH`.

## Cleaning up

```bash
docker compose down -v
```

`-v` also removes the named volumes (and everything logged into them).
