#!/usr/bin/env bash
# 把 fork/patches/ 应用到上游源码树。
#
# 只能从干净的上游提交出发应用一次。重复执行会失败并停在 git am 冲突态
# （git am --3way 对已应用的补丁报冲突，并不会跳过）。
# 冲突时按 `git status` 提示逐块解决后 `git am --continue`；要放弃用 `git am --abort`。
set -euo pipefail

# fork/ 现就在上游仓库根下，默认目标 = fork/ 的上一层
UPSTREAM="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
PATCH_DIR="$(cd "$(dirname "$0")/../patches" && pwd)"

if ! git -C "$UPSTREAM" rev-parse --git-dir >/dev/null 2>&1; then
  echo "error: $UPSTREAM 不是 git 仓库" >&2
  exit 1
fi

cd "$UPSTREAM"

shopt -s nullglob
patches=("$PATCH_DIR"/*.patch)
if [ ${#patches[@]} -eq 0 ]; then
  echo "error: $PATCH_DIR 下没有 .patch 文件" >&2
  exit 1
fi

for patch in "${patches[@]}"; do
  echo "==> applying $(basename "$patch")"
  if git am --3way "$patch" >/dev/null 2>&1; then
    echo "    ok"
  else
    echo "    conflict or already applied: run 'git status' in $UPSTREAM" >&2
    git am --show-current-patch=diff 2>/dev/null | head -5 >&2 || true
    exit 1
  fi
done

echo "done. verify with: $(dirname "$0")/verify.sh"
