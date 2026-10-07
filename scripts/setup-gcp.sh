#!/usr/bin/env bash
# Creates what anyship does not: it deploys the Cloud Run service, its secrets and the Cloud
# Scheduler tick; this script prepares everything around them. Safe to run again.
#
#   scripts/setup-gcp.sh <project> [region]
#   CLOUDSDK_ACTIVE_CONFIG_NAME=anyship-e2e scripts/setup-gcp.sh <your-project-id> asia-east1
#
# Creates: the APIs, the service account the service runs as, the media bucket, the Cloud Tasks
# queue, the permissions between them, and the Artifact Registry remote repository "ghcr" if
# it is missing (Cloud Run pulls ghcr.io images through it).
# Does NOT create: the database (Supabase, see deploy/cloudrun/README.md), the service itself, the
# secrets, or the Scheduler job (all in IaC-deployments/cueflow/anyship.yaml).
#
# Read it before you run it: it changes your project.
set -euo pipefail

PROJECT=${1:?usage: setup-gcp.sh <project> [region]}
REGION=${2:-asia-east1}
BUCKET=${BUCKET:-${PROJECT}-cueflow-media}
QUEUE=${QUEUE:-cueflow}
# How many agent runs / publishes the queue starts at once; keep it equal to
# CUEFLOW_AGENT_CONCURRENCY in the manifest.
CONCURRENCY=${CONCURRENCY:-3}
SA_NAME=${SA_NAME:-cueflow-run}
SA="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"

g() { gcloud --project "$PROJECT" --quiet "$@"; }

echo "== APIs"
g services enable run.googleapis.com cloudtasks.googleapis.com cloudscheduler.googleapis.com \
  secretmanager.googleapis.com storage.googleapis.com iamcredentials.googleapis.com \
  artifactregistry.googleapis.com

echo "== service account $SA"
g iam service-accounts describe "$SA" >/dev/null 2>&1 \
  || g iam service-accounts create "$SA_NAME" --display-name "CueFlow runtime"

echo "== media bucket gs://$BUCKET"
g storage buckets describe "gs://${BUCKET}" >/dev/null 2>&1 \
  || g storage buckets create "gs://${BUCKET}" --location "$REGION" --uniform-bucket-level-access \
       --public-access-prevention
g storage buckets add-iam-policy-binding "gs://${BUCKET}" \
  --member "serviceAccount:${SA}" --role roles/storage.objectAdmin >/dev/null

echo "== permissions of the service account"
# Put work on the queue.
g projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:${SA}" \
  --role roles/cloudtasks.enqueuer --condition=None >/dev/null
# Cloud Tasks deliveries carry an ID token of this same account, so it must be allowed to act as itself.
g iam service-accounts add-iam-policy-binding "$SA" --member "serviceAccount:${SA}" \
  --role roles/iam.serviceAccountUser >/dev/null
# Keyless signed media URLs: the account signs blobs as itself.
g iam service-accounts add-iam-policy-binding "$SA" --member "serviceAccount:${SA}" \
  --role roles/iam.serviceAccountTokenCreator >/dev/null

echo "== Cloud Tasks queue $QUEUE"
if g tasks queues describe "$QUEUE" --location "$REGION" >/dev/null 2>&1; then
  g tasks queues update "$QUEUE" --location "$REGION" \
    --max-concurrent-dispatches "$CONCURRENCY" --max-attempts 5
else
  g tasks queues create "$QUEUE" --location "$REGION" \
    --max-concurrent-dispatches "$CONCURRENCY" --max-attempts 5
fi
# The 30 minute delivery deadline is set by the app on each task; the service's own request
# timeout (30m) comes from the manifest.

echo "== Artifact Registry remote repository \"ghcr\""
if ! g artifacts repositories describe ghcr --location "$REGION" >/dev/null 2>&1; then
  g artifacts repositories create ghcr --location "$REGION" --repository-format docker \
    --mode remote-repository --remote-repo-config-desc "ghcr.io" --remote-docker-repo https://ghcr.io
fi

cat <<EOF

done. Next:
  1. Create the Supabase project and put its SESSION pooler URL and your CUEFLOW_SECRET_KEY into
     Secret Manager from your own terminal (see deploy/cloudrun/README.md).
  2. In IaC-deployments: scripts/deploy.sh cueflow main --dry-run, then --yes.
EOF
