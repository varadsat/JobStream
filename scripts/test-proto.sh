#!/usr/bin/env bash
# Validates the Chunk 0.3 proto definitions and generated Go stubs.
set -euo pipefail

PASS=0
FAIL=0
ROOT=$(git rev-parse --show-toplevel)

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

# ── Proto source files ────────────────────────────────────────────────────────
check "proto/jobstream/v1/application.proto exists" \
  "[ -f '$ROOT/proto/jobstream/v1/application.proto' ]"
check "proto/jobstream/v1/intake.proto exists" \
  "[ -f '$ROOT/proto/jobstream/v1/intake.proto' ]"

check "application.proto declares Source enum" \
  "grep -q 'enum Source' '$ROOT/proto/jobstream/v1/application.proto'"
check "application.proto declares Status enum" \
  "grep -q 'enum Status' '$ROOT/proto/jobstream/v1/application.proto'"
check "application.proto declares Application message" \
  "grep -q 'message Application' '$ROOT/proto/jobstream/v1/application.proto'"
check "application.proto has schema_version field" \
  "grep -q 'schema_version' '$ROOT/proto/jobstream/v1/application.proto'"

check "intake.proto declares IntakeService" \
  "grep -q 'service IntakeService' '$ROOT/proto/jobstream/v1/intake.proto'"
check "intake.proto has SubmitApplication RPC" \
  "grep -q 'rpc SubmitApplication' '$ROOT/proto/jobstream/v1/intake.proto'"
check "intake.proto has BatchSubmitApplication RPC" \
  "grep -q 'rpc BatchSubmitApplication' '$ROOT/proto/jobstream/v1/intake.proto'"
check "intake.proto has GetApplicationStatus RPC" \
  "grep -q 'rpc GetApplicationStatus' '$ROOT/proto/jobstream/v1/intake.proto'"
check "BatchSubmitResult uses oneof outcome" \
  "grep -q 'oneof outcome' '$ROOT/proto/jobstream/v1/intake.proto'"

# ── buf config ───────────────────────────────────────────────────────────────
check "buf.yaml exists" "[ -f '$ROOT/buf.yaml' ]"
check "buf.gen.yaml exists" "[ -f '$ROOT/buf.gen.yaml' ]"
check "buf.yaml references proto module path" \
  "grep -q 'path: proto' '$ROOT/buf.yaml'"

# ── Generated stubs ──────────────────────────────────────────────────────────
check "gen/go/jobstream/v1/application.pb.go exists" \
  "[ -f '$ROOT/gen/go/jobstream/v1/application.pb.go' ]"
check "gen/go/jobstream/v1/intake.pb.go exists" \
  "[ -f '$ROOT/gen/go/jobstream/v1/intake.pb.go' ]"
check "gen/go/jobstream/v1/intake_grpc.pb.go exists" \
  "[ -f '$ROOT/gen/go/jobstream/v1/intake_grpc.pb.go' ]"
check "gen/go/go.mod exists" \
  "[ -f '$ROOT/gen/go/go.mod' ]"
check "gen/go/go.sum exists" \
  "[ -f '$ROOT/gen/go/go.sum' ]"

# ── go.work wires in the gen module ─────────────────────────────────────────
check "go.work uses ./gen/go" \
  "grep -q 'use ./gen/go' '$ROOT/go.work'"

# ── Generated code compiles ──────────────────────────────────────────────────
check "generated package compiles" \
  "go build github.com/varad/jobstream/gen/go/jobstream/v1"

# ── Proto syntax via protoc (if available) ───────────────────────────────────
if command -v protoc &>/dev/null; then
  PROTOC_INCLUDE="${PROTOC_INCLUDE:-$HOME/Downloads/protoc-33.2-win64/include}"
  check "protoc: application.proto is valid" \
    "protoc -I '$ROOT/proto' -I '$PROTOC_INCLUDE' \
       --descriptor_set_out=/dev/null \
       jobstream/v1/application.proto"
  check "protoc: intake.proto is valid" \
    "protoc -I '$ROOT/proto' -I '$PROTOC_INCLUDE' \
       --descriptor_set_out=/dev/null \
       jobstream/v1/application.proto jobstream/v1/intake.proto"
else
  printf 'SKIP  protoc syntax check (protoc not found)\n'
fi

# ── buf lint (if buf available) ───────────────────────────────────────────────
if command -v buf &>/dev/null; then
  check "buf lint passes" "buf lint '$ROOT/proto'"
else
  printf 'SKIP  buf lint (buf not installed — brew install bufbuild/buf/buf)\n'
fi

printf '\nResults: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
