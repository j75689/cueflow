#!/usr/bin/env bash
# One-time (and re-runnable) provisioning of the CueFlow Cloud Run stack. Edit the variables, read
# every command, then run it yourself. It creates: APIs, service accounts, IAM bindings, the GCS
# bucket, the Cloud Tasks queue, secrets (empty shells) and the Cloud Scheduler job. It does NOT
# deploy the service (deploy.sh) nor create the Supabase database.
set -euo pipefail

PROJECT="${PROJECT:-my-project}"
REGION="${REGION:-asia-northeast1}"
SERVICE="${SERVICE:-cueflow}"
BUCKET="${BUCKET:-${PROJECT}-cueflow-media}"
QUEUE="${QUEUE:-cueflow-work}"
AGENT_CONCURRENCY="${AGENT_CONCURRENCY:-3}"
SCHEDULER_JOB="${SCHEDULER_JOB:-cueflow-tick}"
# Cloud Run URL of the service. Leave empty on a first run; the scheduler job is created once
# the service exists (run deploy.sh, then run this script again).
SERVICE_URL="${SERVICE_URL:-}"

RUN_SA_NAME="cueflow-run"
TASKS_SA_NAME="cueflow-tasks"
SCHED_SA_NAME="cueflow-scheduler"
RUN_SA="${RUN_SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
TASKS_SA="${TASKS_SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
SCHED_SA="${SCHED_SA_NAME}@${PROJECT}.iam.gserviceaccount.com"

g() { gcloud --project "$PROJECT" "$@"; }

echo "== enabling APIs"
g services enable run.googleapis.com cloudtasks.googleapis.com cloudscheduler.googleapis.com \
  secretmanager.googleapis.com storage.googleapis.com iamcredentials.googleapis.com \
  artifactregistry.googleapis.com cloudbuild.googleapis.com

echo "== service accounts"
for pair in "$RUN_SA_NAME:CueFlow runtime" "$TASKS_SA_NAME:CueFlow Cloud Tasks caller" "$SCHED_SA_NAME:CueFlow Scheduler caller"; do
  name="${pair%%:*}"; display="${pair#*:}"
  g iam service-accounts describe "${name}@${PROJECT}.iam.gserviceaccount.com" >/dev/null 2>&1 \
    || g iam service-accounts create "$name" --display-name "$display"
done

echo "== bucket"
g storage buckets describe "gs://${BUCKET}" >/dev/null 2>&1 \
  || g storage buckets create "gs://${BUCKET}" --location "$REGION" --uniform-bucket-level-access \
       --public-access-prevention
g storage buckets add-iam-policy-binding "gs://${BUCKET}" \
  --member "serviceAccount:${RUN_SA}" --role roles/storage.objectAdmin >/dev/null

echo "== runtime IAM: enqueue tasks, sign URLs as itself, read secrets"
g projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:${RUN_SA}" \
  --role roles/cloudtasks.enqueuer --condition=None >/dev/null
# Keyless V4 signed URLs: the runtime account signs blobs as itself.
g iam service-accounts add-iam-policy-binding "$RUN_SA" \
  --member "serviceAccount:${RUN_SA}" --role roles/iam.serviceAccountTokenCreator >/dev/null
# Cloud Tasks creates tasks that carry an OIDC token of TASKS_SA: the enqueuer must be allowed to act as it.
g iam service-accounts add-iam-policy-binding "$TASKS_SA" \
  --member "serviceAccount:${RUN_SA}" --role roles/iam.serviceAccountUser >/dev/null

echo "== secrets (shells only; add versions yourself, see README)"
for s in CUEFLOW_SECRET_KEY DATABASE_URL CUEFLOW_ADMIN_TOKEN CUEFLOW_MCP_TOKEN; do
  g secrets describe "$s" >/dev/null 2>&1 || g secrets create "$s" --replication-policy automatic
  g secrets add-iam-policy-binding "$s" --member "serviceAccount:${RUN_SA}" \
    --role roles/secretmanager.secretAccessor >/dev/null
done

echo "== Cloud Tasks queue"
if g tasks queues describe "$QUEUE" --location "$REGION" >/dev/null 2>&1; then
  g tasks queues update "$QUEUE" --location "$REGION" \
    --max-concurrent-dispatches "$AGENT_CONCURRENCY" --max-attempts 5
else
  g tasks queues create "$QUEUE" --location "$REGION" \
    --max-concurrent-dispatches "$AGENT_CONCURRENCY" --max-attempts 5
fi
# The 30 minute dispatch deadline is a per-task attribute (dispatchDeadline, set by the app on every
# task; the HTTP default is 10 minutes). deploy.sh sets --timeout 1800 on the Cloud Run service.
# The service is public at the Cloud Run layer (--allow-unauthenticated): the app itself checks the
# Google ID token on /internal/*, tokens on /api/runner and /mcp, Cloudflare Access on the UI.

if [ -n "$SERVICE_URL" ]; then
  echo "== Cloud Scheduler: tick every minute"
  common=(--location "$REGION" --schedule "* * * * *" --http-method POST
    --uri "${SERVICE_URL%/}/internal/tick"
    --oidc-service-account-email "$SCHED_SA" --oidc-token-audience "${SERVICE_URL%/}"
    --attempt-deadline 60s)
  if g scheduler jobs describe "$SCHEDULER_JOB" --location "$REGION" >/dev/null 2>&1; then
    g scheduler jobs update http "$SCHEDULER_JOB" "${common[@]}"
  else
    g scheduler jobs create http "$SCHEDULER_JOB" "${common[@]}"
  fi
else
  echo "SERVICE_URL not set: skipping the scheduler job (re-run after deploy.sh)"
fi

echo "done"
