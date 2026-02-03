#!/bin/bash
# generate.sh

set -e

echo "Generating Go code from proto files..."

#
# Expect the `sui-apis` repo to exist next to this script.
# If it's missing, fail with a clear message instead of deleting/cloning.
#
PROTO_ROOT="./sui-apis/proto"

if [ ! -d "${PROTO_ROOT}" ]; then
  echo "Error: ${PROTO_ROOT} not found."
  echo "Make sure the 'sui-apis' repo is present at ./sui-apis (e.g. via git submodule or manual clone)."
  exit 1
fi

# Build M options for import path mapping
GO_M_OPTS=""
GRPC_M_OPTS=""
for proto in "${PROTO_ROOT}"/sui/rpc/v2/*.proto; do
  [ -f "$proto" ] || continue
  name=$(basename "$proto")
  GO_M_OPTS="${GO_M_OPTS} --go_opt=Msui/rpc/v2/${name}=github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
  GRPC_M_OPTS="${GRPC_M_OPTS} --go-grpc_opt=Msui/rpc/v2/${name}=github.com/fystack/multichain-indexer/internal/rpc/sui/rpc/v2"
done

protoc \
  -I"${PROTO_ROOT}" \
  --go_out=./internal/rpc \
  --go_opt=paths=source_relative \
  ${GO_M_OPTS} \
  --go-grpc_out=./internal/rpc \
  --go-grpc_opt=paths=source_relative \
  ${GRPC_M_OPTS} \
  "${PROTO_ROOT}"/sui/rpc/v2/*.proto

echo "✓ Code generation complete!"