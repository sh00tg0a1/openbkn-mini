#!/bin/sh
# Redis is a cache for the services; it lives and dies with the container.
# The platform's function executor runs Function and Skill code. mini's sandbox
# stand-in (port 8090) plays its control plane and receives its callbacks; code
# it runs reaches the platform through the gateway.
set -e
mkdir -p /data/oss "$WORKSPACE_PATH"
redis-server --bind 127.0.0.1 --port 6379 --save "" --appendonly no \
  --daemonize yes --logfile /tmp/redis.log
(
  cd /app
  export CONTROL_PLANE_URL=http://127.0.0.1:8090 INTERNAL_API_TOKEN=mini \
    CONTAINER_ID=mini DISABLE_BWRAP=true EXECUTOR_HOST=127.0.0.1 \
    BKN_BASE_URL=http://127.0.0.1:8000 \
    BKN_SANDBOX_MCP_URL=http://127.0.0.1:8000/api/agent-retrieval/v1/mcp/
  exec .venv/bin/python -m executor.interfaces.http.rest
) &
exec /opt/mini/bin/mini
