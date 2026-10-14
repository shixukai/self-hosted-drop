#!/bin/sh
set -eu
# Read Docker secret mounts as root, then permanently drop to the artifact owner.
# The worker receives only its own service credential.
if [ "$1" = 'drop-worker' ]; then
  export DROP_WORKER_KEY="$(cat /run/secrets/worker_key)"
else
  export DROP_INTERNAL_KEY="$(cat /run/secrets/internal_key)"
  export DROP_WORKER_KEY="$(cat /run/secrets/worker_key)"
  export DROP_SETUP_KEY="$(cat /run/secrets/setup_key)"
  export DROP_POSTGRES_PASSWORD="$(cat /run/secrets/postgres_key)"
  if [ -f /run/secrets/restic_key ]; then
    export RESTIC_PASSWORD="$(cat /run/secrets/restic_key)"
  fi
fi
exec su-exec 10001:10001 "$@"
