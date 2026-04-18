#!/usr/bin/env bash
# Validates the Chunk 0.1 scaffold.  Run from the repo root.
set -euo pipefail

PASS=0
FAIL=0
ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

check() {
  local label="$1" cmd="$2"
  if eval "$cmd" &>/dev/null; then
    printf 'PASS  %s\n' "$label"
    PASS=$((PASS + 1))
  else
    printf 'FAIL  %s\n' "$label" >&2
    FAIL=$((FAIL + 1))
  fi
}

# Required directories
for dir in services proto sources dashboard deploy scripts; do
  check "dir exists: $dir/" "[ -d $dir ]"
done

# Required root files
for f in go.work Makefile .editorconfig .gitignore README.md .golangci.yml; do
  check "file exists: $f" "[ -f $f ]"
done

# Git hook
check "file exists: .githooks/pre-commit" "[ -f .githooks/pre-commit ]"
check ".githooks/pre-commit is executable" "[ -x .githooks/pre-commit ]"

# go.work contains a go directive
check "go.work has 'go' directive" "grep -qE '^go [0-9]+\.[0-9]+' go.work"

# Makefile has required targets
for target in proto test lint up down; do
  check "Makefile target: $target" "grep -qE '^$target:' Makefile"
done

printf '\nResults: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
