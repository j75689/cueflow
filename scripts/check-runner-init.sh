#!/usr/bin/env bash
# CI guard: the runner image starts an init (tini) as PID 1 in front of `cueflow runner`, so a process an agent
# left behind is reaped when it exits instead of staying a zombie until the container's pids limit is reached.
# The runner is started as the image starts it, pointed at a server that is not there (it keeps retrying).
# Usage: scripts/check-runner-init.sh [image]   (default cueflow:runner)
set -euo pipefail

image="${1:-cueflow:runner}"
name="cueflow-runner-init-check-$$"
cfg="$(mktemp -d)"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$cfg"' EXIT
printf '{"url":"http://127.0.0.1:9","token":"init-check"}\n' >"$cfg/runner.json"
chmod 0755 "$cfg" && chmod 0644 "$cfg/runner.json"

docker run -d --name "$name" -v "$cfg:/config:ro" "$image" >/dev/null
sleep 2
if [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ]; then
  docker logs "$name" >&2
  echo "the runner container exited" >&2
  exit 1
fi

pid1="$(docker exec "$name" cat /proc/1/comm)"
if [ "$pid1" != tini ]; then
  echo "PID 1 is $pid1, want tini" >&2
  exit 1
fi

# The shell exits at once, so its `sleep 1` is orphaned to PID 1, which must reap it when it exits.
orphan="$(docker exec "$name" sh -c 'sleep 1 </dev/null >/dev/null 2>&1 & echo $!')"
sleep 3
if state="$(docker exec "$name" grep '^State:' "/proc/$orphan/status" 2>/dev/null)"; then
  echo "orphan $orphan was not reaped: $state" >&2
  exit 1
fi
echo "ok: PID 1 is tini and an exited orphan was reaped"
