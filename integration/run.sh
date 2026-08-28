#!/bin/sh
# Runs the integration suite against the live Noetive Semantik API.
#
# The endpoint is not configurable here on purpose. These tests exist to catch
# wire drift — the API changing shape underneath a client that still compiles —
# and pointing them at a stub would defeat that. Only the API key comes from the
# environment.
#
#     NOETIVE_KEY_SECRET=keyu_... integration/run.sh
#
# With no key the suite skips rather than fails, so it is safe to wire into a
# pipeline that does not always have credentials.
#
# The local-embedding tests need somewhere to embed and a namespace to write to.
# They skip when NOETIVE_EMBEDDINGS_URL is unset, and fall back to the shared
# namespace when the routing triple is not given:
#
#     NOETIVE_EMBEDDINGS_URL=http://localhost:11434/v1/embeddings \
#     NOETIVE_NAMESPACE=test NOETIVE_MODEL=Qwen3-Embedding-4B NOETIVE_DIMENSIONS=1024 \
#     integration/run.sh
#
# The endpoint has to answer to the name in NOETIVE_MODEL, which for a local
# runner usually means aliasing it — `ollama cp qwen3-embedding:4b
# Qwen3-Embedding-4B`. A name it does not know comes back as a 404 quoting it.
set -eu

if [ -z "${NOETIVE_KEY_SECRET:-}" ]; then
  echo "NOETIVE_KEY_SECRET is not set; the suite will skip every test." >&2
  echo "Get a key from https://noetive.io/dashboard to run it for real." >&2
fi

cd "$(dirname "$0")/.."

# Cache cleared so a pass never comes from a run against a previous API shape,
# which is the exact failure this suite is meant to notice.
go clean -testcache
exec go test -tags integration -count=1 -timeout 10m -v ./integration/...
