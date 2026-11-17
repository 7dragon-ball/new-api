#!/usr/bin/env bash
# 编译 + go vet + gofmt + 子包测试。
#
# 不跑根包：main.go 有 //go:embed web/dist，而 web/dist 在 .gitignore 里、
# 只在 Dockerfile 第一阶段构建后存在。裸 `go build ./...` 会因此失败。
set -euo pipefail

SRC="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
# 复用同一个 module cache，避免每次重下依赖（首次约 2-4 分钟）
MODCACHE_VOLUME="${GOMOD_VOLUME:-newapi-gomod}"

docker run --rm \
  -v "$SRC":/src \
  -v "$MODCACHE_VOLUME":/go/pkg/mod \
  -w /src \
  -e GOWORK=off \
  -e GOPROXY=https://goproxy.cn,direct \
  golang:1.26.10-alpine sh -c '
    set -e
    echo "==> build"
    go build ./controller/... ./model/... ./service/... ./middleware/... ./router/...
    echo "==> vet"
    go vet ./controller/... ./model/... ./service/...
    echo "==> gofmt"
    unformatted=$(gofmt -l controller model service middleware)
    if [ -n "$unformatted" ]; then
      echo "unformatted files:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    echo "==> test"
    go test ./model/ ./controller/ ./service/ -count=1
  '

echo "verify passed"
