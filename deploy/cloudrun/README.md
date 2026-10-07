# Deploying CueFlow on Google Cloud Run

Serverless layout: Cloud Run (scales to zero) + Supabase Postgres + a GCS bucket for media + Cloud
Tasks (work delivery) + Cloud Scheduler (the one-minute clock). How it works inside:
[`docs/architecture.md` §8.1](../../docs/architecture.md).

The work is split so that each tool does what it is good at:

| Piece | Made by |
| --- | --- |
| Container image `ghcr.io/j75689/cueflow` | `.github/workflows/image.yml` on every push to `main` |
| APIs, service account, media bucket, Cloud Tasks queue, permissions, `ghcr` proxy repository | `scripts/setup-gcp.sh` (this repo; read it, run it yourself, safe to run again) |
| Cloud Run service, its secrets, the Scheduler tick | [anyship](https://github.com/j75689/anyship) (v0.4+) from `cueflow/anyship.yaml` in the IaC-deployments repo |
| Database | Supabase, by hand (below) |
| Custom domain, Cloudflare Access | by hand (below), optional |

The manifest is not in this repo: it holds real project ids and URLs, which live in the deployments
repo. It deploys with `scripts/deploy.sh cueflow main --yes` there.

## 1. Publish the image

Push to `main`; `image.yml` builds the **slim** image (no agent CLIs: the cloud runs agents on
[Runners](../../README.md#run-agents-on-your-own-machine-runners) or as self-driven agents) and
pushes `ghcr.io/j75689/cueflow:main`.

Cloud Run reads ghcr.io through an Artifact Registry remote repository that has no credentials, so
**the package must be public**: after the first push open GitHub, *Packages → cueflow → Package
settings → Change visibility → Public*. The image contains the compiled server and the dashboard
and no secrets, but it does mean the binary is downloadable by anyone while the source stays private.

## 2. Database: Supabase

1. Create a project. Pick the region closest to Cloud Run (the manifest uses `asia-east1`; Tokyo or
   Singapore are the nearest Supabase regions, and every query pays that distance).
2. In *Project settings → Data API*: turn **off** *Enable Data API* and *Automatically expose new
   tables*, and turn **on** *Enable automatic RLS*. CueFlow talks to Postgres directly and never
   uses the REST API, and its tables hold encrypted connector secrets.
3. Use the **session pooler on port 5432**, not the transaction pooler (6543): CueFlow uses prepared
   statements, `LISTEN/NOTIFY` and `FOR UPDATE SKIP LOCKED`, which need session semantics. The
   connection string looks like
   `postgres://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:5432/postgres`.
   Connect as the `postgres` role (it owns the tables, so RLS does not apply to it).
4. `CUEFLOW_DB_MAX_CONNS=5` per instance (set in the manifest) keeps three instances inside the
   free tier's connection limit.
5. Free projects are **paused after 7 days without activity**. The one-minute tick counts as activity;
   if you ever disable it, expect a paused database.

## 3. Google Cloud, once

```sh
CLOUDSDK_ACTIVE_CONFIG_NAME=<your gcloud configuration> scripts/setup-gcp.sh <project> asia-east1
```

It enables the APIs and creates the `cueflow-run` service account (it runs the service, signs the
Cloud Tasks deliveries and is the identity of the Scheduler tick), the media bucket
(`<project>-cueflow-media`, public access prevented), the `cueflow` queue (concurrency 3) and the
`ghcr` remote repository if missing, and grants the account only what it needs: enqueue tasks, manage
objects in that bucket, act as itself for ID tokens and signed URLs.

## 4. Secrets and the first deploy

anyship stores four secrets in Secret Manager as `cueflow-<NAME>`. Two it generates once
(`CUEFLOW_ADMIN_TOKEN`, `CUEFLOW_MCP_TOKEN`); two you provide, **from your own terminal, never
through chat**:

```sh
read -rs v && printf %s "$v" | gcloud --project <project> secrets create cueflow-DATABASE_URL --data-file=- ; unset v
read -rs v && printf %s "$v" | gcloud --project <project> secrets create cueflow-CUEFLOW_SECRET_KEY --data-file=- ; unset v
```

`CUEFLOW_SECRET_KEY` is 32 bytes as 64 hex characters or base64 (`cueflow genkey`). If you restore an
existing database, it must be the **same key** the data was encrypted with, otherwise every stored
connector credential is unreadable. Keep a copy somewhere outside the database.

Then, in the IaC-deployments repo:

```sh
scripts/deploy.sh cueflow main --dry-run   # checks the login, the project, the APIs and the image
scripts/deploy.sh cueflow main --yes
```

`/api/v1/health` is the startup probe: a revision that cannot reach the database never gets traffic
and the previous one keeps serving. The service is public at the Cloud Run layer; the app enforces
auth itself: `CUEFLOW_ADMIN_TOKEN` for the dashboard and API, Google ID tokens (only from
`cueflow-run`) on `/internal/*`, and bearer tokens on `/api/runner/*` and `/mcp*`.

Open the service URL, enter the admin token when asked, and check *Connectors* and *Runners*.

## 5. Moving an existing install

1. Stop the old server, or make it read-only, for a consistent snapshot.
2. Database: `pg_dump --no-owner --no-acl "$OLD_DATABASE_URL" | psql "$SUPABASE_SESSION_POOLER_URL"`.
3. Put the old `CUEFLOW_SECRET_KEY` in Secret Manager (step 4).
4. Media: `DATABASE_URL=... CUEFLOW_SECRET_KEY=... CUEFLOW_MEDIA_STORAGE=gcs://<bucket> cueflow media migrate --to gcs://<bucket>`
   copies the bytes to GCS and updates each row's storage tag. It needs credentials that can write the bucket.
5. Deploy, check `/api/v1/health`, then sign in.

Things that do **not** carry over by themselves:

- Connectors that point at services on your own machine or Docker network (`http://reels-render:8090`,
  `http://news-trends:8080`, a local SearXNG) cannot be reached from Cloud Run: deploy those services too
  (each as its own Cloud Run service) or repoint the connectors.
- Agents with an `acp_agent` runtime run an agent CLI on the CueFlow host, which the slim image lacks.
  Give them a `runner_pool` runtime (your own machine runs the agent) or switch them to the built-in loop.
- Runners must be re-registered against the new URL (`cueflow runner register --url ...`).

## 6. Login with Cloudflare Access (Zero Trust)

Without this the dashboard asks for `CUEFLOW_ADMIN_TOKEN`. With it, people sign in on Cloudflare's
login page (your email, Google, a one-time PIN...) and the dashboard opens without a token.

How it fits together:

```
browser ─► your domain ─► Cloudflare Access (login) ─► Worker ─► Cloud Run service
                                                                  └ checks the Access JWT itself
machines (runners, MCP clients, social platforms) ─► the run.app URL directly (their own tokens)
```

1. **A hostname** on a Cloudflare zone, served by a small Worker that forwards to the service
   (`cueflow/edge/` in the IaC-deployments repo, the same shape as CueSocial's).
2. **An Access application** for that hostname in Zero Trust → Access → Applications, with an Allow
   policy for the people who may sign in. Note its **Application Audience (AUD) tag** and your team
   domain (`<team>.cloudflareaccess.com`).
3. **Tell the service** (in the manifest's `env`):
   - `CUEFLOW_CF_ACCESS_TEAM_DOMAIN` and `CUEFLOW_CF_ACCESS_AUD`: the service then accepts requests that
     carry a valid Access JWT for that application, **or** the admin token (scripts keep working).
   - `CUEFLOW_PUBLIC_URL`: the Access hostname, for links people click (emails, the dashboard).
   - `CUEFLOW_DIRECT_URL`: the `run.app` URL, for what machines use: runner registration commands, the
     MCP setup command shown for agent tokens, and the URL publishers fetch media from.
     Leave `CUEFLOW_TASKS_TARGET_URL` on the `run.app` URL too (Cloud Tasks and Scheduler call it
     directly, and their token audience must match it exactly).

Why the service checks the JWT itself: the `run.app` address stays public and does not pass through
Cloudflare, so trusting "it came through Access" would let anyone skip the login. The dashboard's pages
require the login too, so the `run.app` address shows a visitor only "sign in through Cloudflare Access".
Requests to `/api/v1/*` without a valid Access JWT still need the admin token; `/api/runner/*`, `/mcp*`, `/media/*`
and `/internal/*` have their own tokens and never needed Access, which is why machines use the direct URL
and no bypass rules are needed in Cloudflare.

If the Access session expires while the dashboard is open, requests fail until you reload the page
(Cloudflare answers with a redirect to its login).

## 7. Backups

Supabase's free tier has no point-in-time restore. Dump daily to a bucket, from any machine with the
credentials or a scheduled job:

```sh
pg_dump --format=custom "$DATABASE_URL" | gcloud storage cp - "gs://$BACKUP_BUCKET/cueflow-$(date +%F).dump"
```

Give the backup bucket a lifecycle rule (for example delete after 30 days).

## Costs and limits

At low traffic Cloud Run, Cloud Tasks and Cloud Scheduler stay in their free tiers; the bucket costs
cents; Supabase Free is $0. The service scales to zero but keeps an instance while a request is open,
and the dashboard's live-update stream is a request: leave the dashboard open and the instance stays up.
