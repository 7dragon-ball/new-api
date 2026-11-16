#!/usr/bin/env bash
# 构建 fork 镜像。
#
# 上游 Dockerfile 第一阶段用 bun 构建自带前端 web/dist（main.go 用 go:embed 硬编它），
# 第二阶段才编 Go。因此构建时间包含一次 bun install + 前端构建，不能跳过。
set -euo pipefail

UPSTREAM="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
TAG="${2:-rc.49-tm1}"

cd "$UPSTREAM"

# 构建期要从 registry 拉基础镜像、从 proxy.golang.org 拉 Go 模块，国内网络通常都不通。
# 用 TM_BUILD_PROXY 显式传代理，绝不读 shell 里的 $HTTPS_PROXY——宿主机上代理监听在
# 回环地址，这个地址进了容器指向容器自身，传进去等于没配，且报错很难懂。
# 要填「容器可达的地址」，colima 下一般是宿主内网 IP（如 192.168.x.x）。
PROXY="${TM_BUILD_PROXY:-}"
build_args=()
if [ -n "$PROXY" ]; then
  build_args+=(--build-arg "HTTP_PROXY=${PROXY}" --build-arg "HTTPS_PROXY=${PROXY}")
  echo "==> build proxy: ${PROXY}"
else
  echo "==> build proxy: (none). 若拉取超时，用 TM_BUILD_PROXY=http://<宿主可达IP>:<端口> 重跑"
fi

echo "==> building token-master/new-api:${TAG} from ${UPSTREAM}"
docker build ${build_args[@]+"${build_args[@]}"} -t "token-master/new-api:${TAG}" .

echo "built token-master/new-api:${TAG}"
echo "deploy: set NEWAPI_IMAGE=token-master/new-api:${TAG} in deploy/.env"
