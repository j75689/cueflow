# Deploying CueFlow on Google Cloud Run

Serverless layout: Cloud Run (scales to zero) + Supabase Postgres + a GCS bucket for media + Cloud
Tasks (work delivery) + Cloud Scheduler (the one-minute clock). How it works inside:
[`docs/architecture.md` §8.1](../../docs/architecture.md).

| Piece | Made by |
| --- | --- |
| Container image | published with each [release](https://github.com/j75689/cueflow/releases) (`ghcr.io/j75689/cueflow`) |
| APIs, service account, media bucket, Cloud Tasks queue, permissions | `gcloud`, once (step 3) |
| Cloud Run service, its secrets, the Scheduler tick | `gcloud` (step 4), or any IaC tool that can deploy a Cloud Run service with secrets and a Scheduler job |
| Database | Supabase, by hand (step 2) |
| Custom domain, Cloudflare Access | by hand (step 6), optional |

The commands below use these names; change them if you like, they only have to agree with each other:

```sh
PROJECT=<your project id>
REGION=asia-east1
SA=cueflow-run@$PROJECT.iam.gserviceaccount.com
BUCKET=$PROJECT-cueflow-media
QUEUE=cueflow
g() { gcloud --project "$PROJECT" --quiet "$@"; }
```

## 1. The image

Each release publishes the **slim** image (no agent CLIs: the cloud runs agents on
[Runners](../../docs/external-agents.md#run-agents-on-your-own-machine-runners) or as self-driven agents) as
`ghcr.io/j75689/cueflow:<version>` (and `:latest`). Deploy a version tag such as `1.0.0`.

Cloud Run cannot pull from ghcr.io directly. Either mirror the image into Artifact Registry yourself, or
create an Artifact Registry **remote repository** that proxies ghcr.io (step 3); the package is public, so the
remote repository needs no credentials. If you publish your own images from a fork, the package starts
private and the remote repository cannot read it: set it to public in GitHub (Packages → cueflow →
Package settings → Change visibility).

## 2. Database: Supabase

1. Create a project. Pick the region closest to Cloud Run (for `asia-east1`, Tokyo or Singapore are
   the nearest Supabase regions, and every query pays that distance).
2. In *Project settings → Data API*: turn **off** *Enable Data API* and *Automatically expose new
   tables*, and turn **on** *Enable automatic RLS*. CueFlow talks to Postgres directly and never
   uses the REST API, and its tables hold encrypted connector secrets.
3. CueFlow takes two connection strings (*Connect* in the project, *Connection pooler*):
   - `DATABASE_URL`: the **transaction pooler**, port 6543. It lends a server connection only for the
     length of a transaction and takes ~200 clients on the free tier, so instance count isn't capped
     by the database. With `DATABASE_SESSION_URL` set, CueFlow opens this pool without named prepared
     statements (pgx's `cache_describe` mode), which a transaction pooler can't keep.
   - `DATABASE_SESSION_URL`: the **session pooler**, port 5432, for the work that needs a session:
     each instance's one `LISTEN` connection (live updates across instances) and a short-lived
     connection that takes the migration lock at startup.

   Both look like `postgres://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:<port>/postgres`.
   Connect as the `postgres` role (it owns the tables, so RLS does not apply to it). With only
   `DATABASE_URL` set (a session pooler or a direct connection), everything uses it as before.
4. Connection budget on the free tier: the session pooler's 15 clients go to one `LISTEN` connection
   per instance (plus one for a minute at startup), so about a dozen instances fit, counting the old
   revision's instances that keep running during a deploy until their requests (up to 30 minutes)
   end. `CUEFLOW_DB_MAX_CONNS=5` caps each instance's transaction-pooler clients, far inside its
   limit. Without the session URL every connection is a session client: then keep
   `CUEFLOW_DB_MAX_CONNS=4` and at most three instances, or new ones fail with
   `max clients reached in session mode`.
5. Free projects are **paused after 7 days without activity**. The one-minute tick counts as activity;
   if you ever disable it, expect a paused database.

## 3. Google Cloud, once

Read before you run: this changes your project. Every command is safe to run again.

```sh
g services enable run.googleapis.com cloudtasks.googleapis.com cloudscheduler.googleapis.com \
  secretmanager.googleapis.com storage.googleapis.com iamcredentials.googleapis.com artifactregistry.googleapis.com

# The account the service runs as. It also signs the Cloud Tasks deliveries and is the identity of the Scheduler tick.
g iam service-accounts create cueflow-run --display-name "CueFlow runtime"

# Media bucket, never public: /media/{id} answers with short-lived signed URLs.
g storage buckets create "gs://$BUCKET" --location "$REGION" --uniform-bucket-level-access --public-access-prevention
g storage buckets add-iam-policy-binding "gs://$BUCKET" --member "serviceAccount:$SA" --role roles/storage.objectAdmin

# Background work. Keep --max-concurrent-dispatches equal to CUEFLOW_AGENT_CONCURRENCY.
g tasks queues create "$QUEUE" --location "$REGION" --max-concurrent-dispatches 3 --max-attempts 5

# Only what the account needs: enqueue tasks, act as itself for ID tokens, sign media URLs without a key file.
g projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$SA" --role roles/cloudtasks.enqueuer --condition=None
g iam service-accounts add-iam-policy-binding "$SA" --member "serviceAccount:$SA" --role roles/iam.serviceAccountUser
g iam service-accounts add-iam-policy-binding "$SA" --member "serviceAccount:$SA" --role roles/iam.serviceAccountTokenCreator

# Lets Cloud Run pull ghcr.io images as $REGION-docker.pkg.dev/$PROJECT/ghcr/j75689/cueflow:<version>
g artifacts repositories create ghcr --location "$REGION" --repository-format docker \
  --mode remote-repository --remote-repo-config-desc "ghcr.io" --remote-docker-repo https://ghcr.io
```

## 4. Secrets and the first deploy

Five secrets, created **from your own terminal, never through chat**. `CUEFLOW_SECRET_KEY` is 32 bytes
as 64 hex characters or base64 (`cueflow genkey`). If you restore an existing database, it must be the
**same key** the data was encrypted with, otherwise every stored connector credential is unreadable.
Keep a copy somewhere outside the database.

```sh
for name in DATABASE_URL DATABASE_SESSION_URL CUEFLOW_SECRET_KEY CUEFLOW_ADMIN_TOKEN CUEFLOW_MCP_TOKEN; do
  printf '%s: ' "$name"; read -rs v; echo
  printf %s "$v" | g secrets create "cueflow-$name" --data-file=- ; unset v
  g secrets add-iam-policy-binding "cueflow-$name" --member "serviceAccount:$SA" --role roles/secretmanager.secretAccessor
done
```

Deploy. The service's own URL is needed in its environment (Cloud Tasks and Scheduler call it, and
their token audience must match it exactly), so deploy once, read the URL, and deploy again with it:

```sh
IMAGE=$REGION-docker.pkg.dev/$PROJECT/ghcr/j75689/cueflow:1.0.0   # a release
g run deploy cueflow --region "$REGION" --image "$IMAGE" --service-account "$SA" \
  --allow-unauthenticated --min-instances 0 --max-instances 3 --timeout 1800 \
  --set-secrets "DATABASE_URL=cueflow-DATABASE_URL:latest,DATABASE_SESSION_URL=cueflow-DATABASE_SESSION_URL:latest,CUEFLOW_SECRET_KEY=cueflow-CUEFLOW_SECRET_KEY:latest,CUEFLOW_ADMIN_TOKEN=cueflow-CUEFLOW_ADMIN_TOKEN:latest,CUEFLOW_MCP_TOKEN=cueflow-CUEFLOW_MCP_TOKEN:latest" \
  --set-env-vars "CUEFLOW_QUEUE=cloudtasks,CUEFLOW_TICK=external,CUEFLOW_TASKS_QUEUE=projects/$PROJECT/locations/$REGION/queues/$QUEUE,CUEFLOW_TASKS_SERVICE_ACCOUNT=$SA,CUEFLOW_INTERNAL_SERVICE_ACCOUNTS=$SA,CUEFLOW_MEDIA_STORAGE=gcs://$BUCKET/media,CUEFLOW_DB_MAX_CONNS=5,CUEFLOW_AGENT_CONCURRENCY=3"

URL=$(g run services describe cueflow --region "$REGION" --format 'value(status.url)')
g run services update cueflow --region "$REGION" \
  --update-env-vars "CUEFLOW_PUBLIC_URL=$URL,CUEFLOW_TASKS_TARGET_URL=$URL"

# The clock: one POST /internal/tick per minute, signed as cueflow-run.
g scheduler jobs create http cueflow-tick --location "$REGION" --schedule '* * * * *' \
  --uri "$URL/internal/tick" --http-method POST \
  --oidc-service-account-email "$SA" --oidc-token-audience "$URL"
```

`/api/v1/health` is the startup probe: a revision that cannot reach the database never gets traffic
and the previous one keeps serving. The service is public at the Cloud Run layer; the app enforces
auth itself: `CUEFLOW_ADMIN_TOKEN` for the dashboard and API, Google ID tokens (only from
`cueflow-run`) on `/internal/*`, and bearer tokens on `/api/runner/*` and `/mcp*`.

Open the service URL, enter the admin token when asked, and check *Connectors* and *Runners*.

If you prefer Terraform or another deployment tool, the resources above are the whole list: the
service with those secrets and variables, and the Scheduler job.

An install that still runs everything through the session pooler moves to the transaction pooler
with two new secrets and one deploy. Leave `cueflow-DATABASE_URL` as it is: Cloud Run reads a
secret when an instance starts, so changing it in place would hand the running revision's new
instances a transaction-pooler URL without the session URL (and it is the way back):

```sh
# 1. The session URL is what DATABASE_URL holds today (port 5432).
g secrets versions access latest --secret cueflow-DATABASE_URL |
  g secrets create cueflow-DATABASE_SESSION_URL --data-file=-
# 2. The transaction pooler: the same URL with port 6543.
printf 'DATABASE_URL on port 6543: '; read -rs v; echo
printf %s "$v" | g secrets create cueflow-DATABASE_POOLED_URL --data-file=- ; unset v
for name in DATABASE_SESSION_URL DATABASE_POOLED_URL; do
  g secrets add-iam-policy-binding "cueflow-$name" --member "serviceAccount:$SA" --role roles/secretmanager.secretAccessor
done
# 3. One deploy switches both.
g run services update cueflow --region "$REGION" \
  --update-secrets "DATABASE_URL=cueflow-DATABASE_POOLED_URL:latest,DATABASE_SESSION_URL=cueflow-DATABASE_SESSION_URL:latest" \
  --update-env-vars CUEFLOW_DB_MAX_CONNS=5
```

To go back, map `DATABASE_URL` to `cueflow-DATABASE_URL` again and remove `DATABASE_SESSION_URL`.

## 5. Moving an existing install

1. Stop the old server, or make it read-only, for a consistent snapshot.
2. Database: `pg_dump --no-owner --no-acl "$OLD_DATABASE_URL" | psql "$SUPABASE_SESSION_POOLER_URL"`.
3. Put the old `CUEFLOW_SECRET_KEY` in Secret Manager (step 4).
4. Media: `DATABASE_URL=... CUEFLOW_SECRET_KEY=... CUEFLOW_MEDIA_STORAGE=gcs://<bucket> cueflow media migrate --to gcs://<bucket>`
   copies the bytes to GCS and updates each row's storage tag. It needs credentials that can write the bucket.
5. Deploy, check `/api/v1/health`, then sign in.

Things that do **not** carry over by themselves:

- Connectors that point at services on your own machine or Docker network (an MCP server at
  `http://some-service:8080`, a local SearXNG) cannot be reached from Cloud Run: deploy those services too
  (each as its own private Cloud Run service, called with CueFlow's Google identity, a connector option) or
  repoint the connectors.
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

1. **A hostname** on a Cloudflare zone, served by a small Worker that forwards to the service. The
   whole Worker is a hostname rewrite; Cloudflare adds the Access JWT header before it runs:

   ```js
   export default {
     fetch(request) {
       const url = new URL(request.url);
       url.hostname = "<service>.run.app";
       return fetch(new Request(url, request));
     },
   };
   ```

2. **An Access application** for that hostname in Zero Trust → Access → Applications, with an Allow
   policy for the people who may sign in. Note its **Application Audience (AUD) tag** and your team
   domain (`<team>.cloudflareaccess.com`).
3. **Tell the service** (environment variables):
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

A request lasts at most 30 minutes (`--timeout 1800`, and Cloud Tasks' dispatch deadline). Agent runs
that take longer continue across deliveries: built-in runs in a fresh run, runner runs handed over
with the runner's agent still working (see [agentic-flow §6.2](../../docs/agentic-flow.md#62-how-runs-end)).

## Services that write media to the bucket

A service of yours that produces big files (a video renderer, for example) should not push them through
CueFlow. Give it its own service account with `roles/storage.objectCreator` on the bucket, restricted to the
`uploads/` prefix:

```sh
g iam service-accounts create cueflow-render --display-name "CueFlow media producer"
g storage buckets add-iam-policy-binding "gs://$BUCKET" \
  --member "serviceAccount:cueflow-render@$PROJECT.iam.gserviceaccount.com" --role roles/storage.objectCreator \
  --condition="title=uploads-only,expression=resource.name.startsWith(\"projects/_/buckets/$BUCKET/objects/uploads/\")"
```

Let it write `uploads/<uuid>.mp4`, then have it call `POST /internal/media/register` with a Google ID token
(audience = the CueFlow service URL). Put that account in `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`, and add a
bucket lifecycle rule that deletes `uploads/` objects after one day (they are only there until registered).
Set `CUEFLOW_MEDIA_RETENTION_DAYS` to have old, unused media pruned on every tick. See `docs/api.md`.
