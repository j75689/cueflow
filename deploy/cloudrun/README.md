# Deploying CueFlow on Google Cloud Run

Serverless layout: Cloud Run (scale to zero) + Supabase Postgres + a GCS bucket for media + Cloud Tasks
(work delivery) + Cloud Scheduler (the one-minute clock). Everything below uses `asia-northeast1`
(Tokyo). `setup.sh` and `deploy.sh` in this directory perform steps 2 to 7; read them before running.

## 1. Database: Supabase (Tokyo)

1. Create a Supabase project in the Tokyo region.
2. Connect through the **session pooler on port 5432**, not the transaction pooler (6543): CueFlow uses
   prepared statements, advisory locks and `FOR UPDATE SKIP LOCKED`, which need session semantics.
   Connection string: `postgres://postgres.<ref>:<password>@aws-0-ap-northeast-1.pooler.supabase.com:5432/postgres`.
3. Keep the pool small: `CUEFLOW_DB_MAX_CONNS=5` (3 instances at most x 5 stays inside the free tier limits).
4. Free tier projects are **paused after 7 days without activity**. The one-minute tick keeps it
   active, but if you scale the scheduler down or disable it, expect a paused database; restore it
   from the Supabase dashboard.

## 2. Project, APIs, service accounts

`setup.sh` enables the APIs and creates three service accounts:

| Account | Purpose | Roles |
| --- | --- | --- |
| `cueflow-run` | Cloud Run runtime identity | `storage.objectAdmin` on the bucket, `cloudtasks.enqueuer`, `secretmanager.secretAccessor` on the four secrets, `iam.serviceAccountTokenCreator` on itself (keyless signed URLs), `iam.serviceAccountUser` on `cueflow-tasks` |
| `cueflow-tasks` | signs the OIDC token on each Cloud Tasks delivery | none (identity only) |
| `cueflow-scheduler` | signs the OIDC token of the Scheduler tick | none (identity only) |

## 3. GCS bucket

`setup.sh` creates `gs://$BUCKET` (uniform access, public access prevention on). Media is served
through the app (or short-lived signed URLs), never publicly.

## 4. Secrets (Secret Manager)

`setup.sh` creates empty secrets; add the values:

```bash
printf '%s' "$(openssl rand -hex 32)"   | gcloud secrets versions add CUEFLOW_SECRET_KEY --data-file=-
printf '%s' "$DATABASE_URL"             | gcloud secrets versions add DATABASE_URL --data-file=-
printf '%s' "$(openssl rand -hex 24)"   | gcloud secrets versions add CUEFLOW_ADMIN_TOKEN --data-file=-
printf '%s' "$(openssl rand -hex 24)"   | gcloud secrets versions add CUEFLOW_MCP_TOKEN --data-file=-
```

If you are migrating an existing install, `CUEFLOW_SECRET_KEY` must be the **same key** the old
database was encrypted with, otherwise stored credentials cannot be decrypted.

## 5. Cloud Tasks queue

`setup.sh` creates `cueflow-work` with `--max-concurrent-dispatches` equal to
`CUEFLOW_AGENT_CONCURRENCY` (default 3) so the queue, not the instance count, bounds parallel agent
runs. Each task carries a 30 minute `dispatchDeadline` (the maximum), and the service is deployed
with `--timeout 1800` so a long agent run is not cut off.

## 6. Cloud Run service

```bash
cp env.example env.yaml   # convert to a YAML map (KEY: "value"), plain env vars only
./deploy.sh
```

`deploy.sh` builds the slim image with Cloud Build and runs `gcloud run deploy` with
`--min-instances 0 --max-instances 3`, the secrets mounted from Secret Manager and the runtime
account. The service allows unauthenticated invocation at the Cloud Run layer; the app enforces auth
itself (Google ID tokens on `/internal/*`, bearer tokens on `/api/runner` and `/mcp`).

Environment variables (see `env.example`):

```
CUEFLOW_QUEUE=cloudtasks
CUEFLOW_TICK=external
CUEFLOW_MEDIA_STORAGE=gcs://BUCKET/PREFIX
CUEFLOW_TASKS_QUEUE=projects/P/locations/asia-northeast1/queues/cueflow-work
CUEFLOW_TASKS_TARGET_URL=https://<service>-<hash>-an.a.run.app
CUEFLOW_TASKS_SERVICE_ACCOUNT=cueflow-tasks@P.iam.gserviceaccount.com
CUEFLOW_INTERNAL_SERVICE_ACCOUNTS=cueflow-tasks@P...,cueflow-scheduler@P...
CUEFLOW_DB_MAX_CONNS=5
```

## 7. Cloud Scheduler tick

Once the service has a URL, re-run `SERVICE_URL=https://... ./setup.sh`. It creates the
`cueflow-tick` job: every minute, `POST $SERVICE_URL/internal/tick`, authenticated with an OIDC token
of `cueflow-scheduler` and audience `$SERVICE_URL`. With `CUEFLOW_TICK=external` no in-process
loops run, so this job is the only clock (cron posts, schedules, retries, digests).

## 8. Cloudflare Access

Put the custom hostname behind Cloudflare Access for the dashboard, with **bypass** policies for the
paths that authenticate themselves:

- `/api/runner/*` (runner agent token)
- `/mcp*` (MCP token)
- `/media/*` (media URLs fetched by social platforms)
- `/internal/*` (Google OIDC; Cloud Tasks and Scheduler call the `run.app` URL directly, so this
  only matters if you point `CUEFLOW_TASKS_TARGET_URL` at the Cloudflare hostname)

Set `CUEFLOW_PUBLIC_URL` to the Cloudflare hostname.

## 9. Migrating an existing install

1. Stop the old server (or make it read-only) to get a consistent snapshot.
2. Database: `pg_dump --no-owner --no-acl "$OLD_DATABASE_URL" | psql "$SUPABASE_DIRECT_URL"`
   (use the direct 5432 connection or the session pooler). Keep the same `CUEFLOW_SECRET_KEY`.
3. Media: `cueflow media migrate --to gcs://BUCKET/PREFIX` copies media bytes to GCS and updates
   each row's storage tag.
4. Deploy, check `/healthz`, then switch DNS / the Cloudflare origin.

## 10. Backups

Supabase free tier has no point-in-time restore. Run a daily dump to GCS, for example from Cloud
Run Jobs or any machine with credentials:

```bash
pg_dump --format=custom "$DATABASE_URL" | gcloud storage cp - "gs://$BACKUP_BUCKET/cueflow-$(date +%F).dump"
```

Give the backup bucket a lifecycle rule (for example delete after 30 days) and trigger the job from
a second Cloud Scheduler entry. Keep a copy of `CUEFLOW_SECRET_KEY` somewhere outside the database:
a backup is useless without it.
