#!/usr/bin/env bash
set -euo pipefail

set -a
source /etc/clusterguard/agent.env
set +a

exec /usr/local/bin/clusterguard-agent "$@"
