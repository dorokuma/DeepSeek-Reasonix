#!/usr/bin/env bash
set -euo pipefail
payload=$(cat)
limit=$(echo "$payload" | jq -r '.toolArgs.limit // empty')
if [ -n "$limit" ]; then echo "$payload"; exit 0; fi
if command -v rtk &>/dev/null; then
  def=${REASONIX_RTK_READ_LIMIT:-800}
else
  def=200
fi
echo "$payload" | jq --argjson limit "$def" '.toolArgs.limit = $limit'
exit 0
