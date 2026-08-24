#!/bin/sh
set -e
# Create/update the superuser on boot so the dashboard at /_/ is reachable
# without console access. Credentials come from the seekout-superuser secret.
if [ -n "$SEEKOUT_SUPERUSER_EMAIL" ] && [ -n "$SEEKOUT_SUPERUSER_PASSWORD" ]; then
    /seekout superuser upsert "$SEEKOUT_SUPERUSER_EMAIL" "$SEEKOUT_SUPERUSER_PASSWORD" --dir=/pb/pb_data
fi
exec "$@"
