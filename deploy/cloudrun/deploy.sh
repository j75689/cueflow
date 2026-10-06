#!/usr/bin/env bash
# Build the slim image and deploy it to Cloud Run. Run from anywhere; it builds the repo root.
set -euo pipefail

PROJECT="${PROJECT:-my-project}"
REGION="${REGION:-asia-northeast1}"
SERVICE="${SERVICE:-cueflow}"
REPO="${REPO:-cueflow}"                 # Artifact Registry docker repository
TAG="${TAG:-$(git rev-parse --short HEAD)}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/${REPO}/cueflow:${TAG}"
ENV_FILE="${ENV_FILE:-$(dirname "$0")/env.yaml}"   # YAML map of plain env vars, see env.example
RUN_SA="cueflow-run@${PROJECT}.iam.gserviceaccount.com"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

gcloud --project "$PROJECT" artifacts repositories describe "$REPO" --location "$REGION" >/dev/null 2>&1 \
  || gcloud --project "$PROJECT" artifacts repositories create "$REPO" --location "$REGION" --repository-format docker

# Cloud Build builds the Dockerfile's default (slim) target; no local Docker needed.
gcloud --project "$PROJECT" builds submit "$ROOT" --tag "$IMAGE"

[ -f "$ENV_FILE" ] || { echo "missing $ENV_FILE (copy env.example values into a YAML map KEY: value)"; exit 1; }

gcloud --project "$PROJECT" run deploy "$SERVICE" \
  --image "$IMAGE" --region "$REGION" \
  --service-account "$RUN_SA" \
  --allow-unauthenticated \
  --min-instances 0 --max-instances 3 \
  --cpu 1 --memory 512Mi --concurrency 20 \
  --timeout 1800 \
  --env-vars-file "$ENV_FILE" \
  --set-secrets "CUEFLOW_SECRET_KEY=CUEFLOW_SECRET_KEY:latest,DATABASE_URL=DATABASE_URL:latest,CUEFLOW_ADMIN_TOKEN=CUEFLOW_ADMIN_TOKEN:latest,CUEFLOW_MCP_TOKEN=CUEFLOW_MCP_TOKEN:latest"

gcloud --project "$PROJECT" run services describe "$SERVICE" --region "$REGION" --format 'value(status.url)'
