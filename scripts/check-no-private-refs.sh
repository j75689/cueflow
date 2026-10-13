#!/usr/bin/env bash
# CI guard: the public tree must not name the maintainer's private infrastructure (deployment
# repositories, in-house helper services, project ids). The generic words live here; anything that
# would itself be a leak (a project id, a brand) comes from the PRIVATE_REF_WORDS repository
# variable as an extra `|`-separated list, so the script never has to spell it.
set -euo pipefail
cd "$(dirname "$0")/.."

WORDS='IaC-deployments|anyship|news-trends|reels-render|lessons-runner|lessons runner'
WORDS="${WORDS}${PRIVATE_REF_WORDS:+|$PRIVATE_REF_WORDS}"

if git grep -nIiE -- "$WORDS" -- ':!scripts/check-no-private-refs.sh' ':!web/package-lock.json'; then
  echo
  echo "private references found (see above). Replace them with a placeholder or a generic description." >&2
  exit 1
fi
echo "ok: no private references"
