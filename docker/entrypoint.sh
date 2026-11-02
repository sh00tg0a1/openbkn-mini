#!/bin/sh
# Redis is a cache for the services; it lives and dies with the container.
set -e
mkdir -p /data/oss
redis-server --bind 127.0.0.1 --port 6379 --save "" --appendonly no \
  --daemonize yes --logfile /tmp/redis.log
exec /app/bin/mini
