#!/usr/bin/env bash
set -euo pipefail
if ! command -v rtk &>/dev/null; then cat; exit 0; fi
if ! command -v jq &>/dev/null; then cat; exit 0; fi
payload=$(cat)
cmd=$(echo "$payload" | jq -r '.toolArgs.command // empty')
if [ -z "$cmd" ]; then echo "$payload"; exit 0; fi
first=$(echo "$cmd" | awk '{print $1}')
case "$first" in curl|wget|ssh|scp|rsync|nc|telnet|ftp|sftp) echo "$payload"; exit 0;; esac
rewritten=$(rtk rewrite "$cmd" 2>/dev/null || true)
if [ -n "$rewritten" ] && [ "$rewritten" != "$cmd" ]; then
  echo "$payload" | jq --arg cmd "$rewritten" '.toolArgs.command = $cmd'
else
  echo "$payload"
fi
exit 0
