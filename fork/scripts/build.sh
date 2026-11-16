#!/usr/bin/env bash
# 构建 fork 镜像。
#
# 上游 Dockerfile 第一阶段用 bun 构建自带前端 web/dist（main.go 用 go:embed 硬编它），
# 第二阶段才编 Go。因此构建时间包含一次 bun install + 前端构建，不能跳过。
set -euo pipefail

UPSTREAM="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
TAG="${2:-rc.49-tm1}"

cd "$UPSTREAM"

echo "==> building token-master/new-api:${TAG} from ${UPSTREAM}"
docker build -t "token-master/new-api:${TAG}" .

echo "built token-master/new-api:${TAG}"
echo "deploy: set NEWAPI_IMAGE=token-master/new-api:${TAG} in deploy/.env"
