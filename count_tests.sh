#!/bin/bash
cd /root/reasonix
for pkg in $(go list ./... 2>/dev/null); do
  count=$(go test "$pkg" -list ".*" 2>/dev/null | grep -c "^Test" || true)
  echo "$count	$pkg"
done
