# fork/ — New API 的定制登记册

本仓库是 [New API](https://github.com/QuantumNous/new-api)（AI API 网关）的 fork，在上面做了三组
后端定制：支持多个超级管理员（root）、在 `users` 表记录真实的最近调用时刻、把部门归属从业务库
搬到 `new_api` 库。

**定制直接写在源码里，以 `// [TM-FORK]` 标记。** 没有补丁目录，也没有 `git am` 重放步骤。
本文件是唯一的定制登记册：哪几处与上游不同、为什么不同、merge 上游时怎么保住它们。

## 为什么改成直接改源码

早期的做法是 `git format-patch` 导出补丁、升级时 `git am --3way` 重放。它有三个实际代价：

- **补丁与源码树天然会漂**。补丁导出后源码又改过，补丁就再也应用不上。本仓库曾同时存在
  「补丁清单说 15 个」和「源码树只应用了 2 个」的错位状态，读文档的人无法判断真实代码。
- **重放失败要人工分辨「已应用」还是「真冲突」**。`git am` 对已应用的补丁报 `conflict or
  already applied`，与真冲突同一种报错，误判一次就要 `git am --abort` 收拾残局。
- **上游前进后旧补丁整体失效**。补丁记的是导出时的上游上下文，上游一重构，全部补丁同时变红，
  而不是只有真正冲突的那几块变红。

直接改源码之后，merge 时 git 的合并算法按行处理，只有真正重叠的行才冲突。实测（2026-11-16）：
从基线 `6ef123cd5` 合并 `upstream/main` 的 11 个提交、63 个文件，**零冲突**——上游这段时间没有
碰我们改过的任何一个文件。这是直接改源码的常态收益，补丁方式做不到。

代价是 `// [TM-FORK]` 标记成了唯一线索，因此每处改动都必须在标记的上一行说清「为什么和上游不同」。

## 仓库布局

```
fork/
├── README.md          # 本文件：定制登记册 + merge 流程
└── scripts/
    ├── verify.sh      # 容器内编译 + vet + gofmt + 子包测试
    └── build.sh       # docker build 出镜像
```

上游自带的 `docs/` 与本目录无关，需要的信息都在这里。

## 分支与远端

| 名字 | 作用 |
|---|---|
| `upstream` | 上游 `QuantumNous/new-api`，**只读**，用来 merge |
| `origin` | 自己的 fork `7dragon-ball/new-api`，定制提交推这里 |

`upstream` 不是 clone 自带的，需要手动添加（见下方「上游升级」）。

定制的三个提交，按依赖顺序，每个对应一组：

```
da9b37f5c  feat(fork): 多 root 管理员与 fork 脚手架
ac47e2383  feat(fork): 在 users 表记录真实的最近调用时刻
522c044f9  feat(fork): 部门归属搬进 new_api 库
```

基线是上游 `6ef123cd5`。这三个提交是**可直接 merge 的源码改动**，不是补丁。

## 定制清单

### 一、多 root 管理员（`da9b37f5c`）

**上游行为**：`role=100`（root / 超级管理员）在系统里唯一——控制器里有多处硬编码把「root 唯一」
写死，没有任何接口能产出或撤销第二个 `role=100` 账号。

**需求**：管理员可以有多个。产品语义里「管理员」就是 `role=100`，因此需要放开唯一性。

**做法**：不删掉唯一性假设，而是把它换成一个正确的不变量：

> 任何把某个「`role=100` 且状态启用且未软删除」的账号改成非 root、改成禁用、或删除的操作，
> 都要求此刻启用的 root 数 > 1。

守卫在事务内、在任何写之前执行，并锁住整组启用中的 root 行（`SELECT ... FOR UPDATE`，按 id
排序以避免死锁），而不是只做一次 `COUNT`。两个并发请求各自只读计数会双双通过；加行锁后第二个
事务会阻塞到第一个提交，随后读到 1 并被拒绝。

控制器上做最小放开：允许 root 创建 root、允许把普通用户提为 root / 撤销 root。`role=10`（普通
管理员）的 `promote`/`demote` 保持上游行为不变，多 root 走新增的 `grant_root`/`revoke_root`
两个 action，属纯增量。

### 二、最近调用时刻（`ac47e2383`）

**上游行为**：`quota_data.created_at` 在写入时就被抹成整点（`created_at - created_at % 3600`），
所以按用量流水推算「最近一次调用」只能得到 `xx:00`。`users.last_login_at` 是登录时间，不是调用时间。

**做法**：`users` 表新增 `last_called_at`，在已经发生的扣费 UPDATE 里同行写入秒级时间戳，不增加
数据库往返。

**难点是区分「调用」与「非调用」**。同一个 UPDATE 落点承担三类写入：真实计费、管理员充值/兑换码、
任务退款结算。只有第一类算调用。判定方式是看 `request_count`：一次调用必然伴随一次请求数累加，
退款是相反的减法，充值不动请求数。

> **本仓库前端未改动。** `web/` 与上游一致，自带控制台不显示这一列。该字段供自行开发的管理端读取。

### 三、部门归属（`522c044f9`）

**背景**：部门原本存在业务库 `token_master` 的 `department` 表，与账号数据分离在两个库，任何
「按部门筛选用户」「按部门聚合用量」都要跨库拼装。

**做法**：把部门字典搬进 `new_api` 库（`department` 表 + `users.department_id`），让归属成为账号
自己的属性，筛选与聚合都能在一条 SQL 里完成。

- **有意不加外键**：外键的 RESTRICT 看不见 `deleted_at`，被软删除的用户仍会挡住部门删除。
  「还有几个用户」这条检查由 `DeleteDepartment` 自己数未软删的行。
- `users.department_id` 声明成 `*int` 而不是 `int`：GORM 零值跳过会让可空 FK 写不进 NULL。
- **不复用 `PUT /api/user/` 改部门**：那是上游自带前端在用的整行替换接口，请求体不带部门时会被
  整行覆盖清空。部门改归属走独立的 `PUT /api/user/:id/department`。
- 部门用量聚合的三个数据源各自聚合成 CTE 再 LEFT JOIN：摊平成多表 JOIN 会做
  `users × tokens × quota_data` 笛卡尔积，有 N 把秘钥的用户用量被放大 N 倍。

**发布顺序**：必须先部署含这些接口的新版 BFF，再执行删掉 `token_master.department` 与
`user_profile.department_id` 的 goose 迁移。旧版 BFF 仍在读写那两列，列一删就立刻报错。

## 改了哪些文件

共 22 个文件（9 个被改、13 个新增），+2101/-62。

**上游文件（merge 时唯一可能冲突的地方）**

| 文件 | 改动 | 风险 |
|---|---|---|
| `model/user.go` | 不变量辅助函数、`UpdateWithTx`/`delete` 的守卫、`LastCalledAt` 与 `DepartmentId` 字段、三条扣费写入路径、`SearchUsers` 的筛选条件 | **最高**，三组定制都改它 |
| `controller/user.go` | `CreateUser` 允许 root 建 root 并接受部门；`ManageUser` 删两处硬拒、新增两个 action、扩写 demote 分支；`SearchUsers` 加筛选；新增 `UpdateUserDepartment` | **高** |
| `router/api-router.go` | 三处插路由：`/api/department/`、`PUT /api/user/:id/department`、`GET /api/data/departments` | 中，三组都插在同一文件 |
| `controller/audit.go` | 审计 action 英文模板 map 加 4 项 | 低 |
| `service/security_verification.go` | proof action 白名单加 2 项 | 低 |
| `model/main.go` | `AutoMigrate` 列表加 `&Department{}` | 低 |
| `model/task_cas_test.go` | 测试的 `AutoMigrate` 同样加 `&Department{}` | 低 |
| `model/user_pagination_test.go` | `SearchUsers` 签名变化，补两个参数 | 低 |
| `controller/security_enrollment_test.go` | 绑定清单加用例 | 低 |

**本 fork 新增（上游不可能改，merge 时不会冲突）**

`model/department.go`、`controller/department.go`、`controller/department_usage.go`，以及 10 个
新增的 `*_test.go`（另有 3 个上游测试文件被改签名或补迁移）。上游若在这些路径放了同名文件，
git 会报 `add/add` 冲突——这是唯一需要留意的情形。

`model/user.go` 里有一段 `User` 结构体的整段对齐格式化 diff（加字段导致列宽变化），属噪声，
不影响行为，但会让该文件的 diff 看起来比实际改动大。

## 标记约定

每处改动或紧邻的一组改动上方（只标改动块，不给整个函数加标记）：

```go
// [TM-FORK] <一句话说明为什么这块和上游不同>
```

当前共 46 处、10 个文件：

| 文件 | 处数 |
|---|---|
| `model/user.go` | 14 |
| `controller/user.go` | 11 |
| `model/user_last_called_test.go` | 6 |
| `controller/department.go` | 5 |
| `model/department.go` | 3 |
| `router/api-router.go` | 3 |
| `controller/audit.go`、`controller/department_usage.go`、`controller/security_enrollment_test.go`、`service/security_verification.go` | 各 1 |

```bash
grep -rn "\[TM-FORK\]" --include="*.go" .
```

`model/main.go` 的 `&Department{}` 与 `model/user_pagination_test.go`、`model/task_cas_test.go`
的改动没有加标记——它们是机械连带（表要迁移、签名要跟着改），单看那一行就能看懂，加标记反而
增加 merge 冲突面。判断是否漏改时，用上表的文件清单核对，不要只看 grep 结果。

## 上游升级

### 首次配置 upstream

```bash
git remote add upstream https://github.com/QuantumNous/new-api.git
```

### 常规升级

```bash
git fetch upstream main --no-tags
git checkout -b upgrade/<日期或上游提交号>
git merge upstream/main
fork/scripts/verify.sh
```

merge 无冲突时直接进验证。有冲突时按下面的原则处理。

### 冲突处理原则

按概率降序：`model/user.go` → `controller/user.go` 的 `ManageUser` switch → `router/api-router.go`
→ `SearchUsers`/`GetAllUsers`（部门筛选改了查询构造）→ 白名单与审计 map。

- **先接受上游版本，再按 `// [TM-FORK]` 逐块重落。** 不要保留整段旧代码试图「合并语义」。
- 守卫**永远**在事务内、永远在写之前，不因上游重构挪到控制器。
- `adminUserManageActions` 是白名单，上游新增 action 时**取并集**。
- 部门相关的手写 SQL（删除计数、用量聚合）都写 `department`（单数），
  `Department.TableName()` 必须保持单数，否则上游若改动默认命名策略会静默错表。
- `SearchUsers` 的参数列表是定制扩展过的。上游若重构它（例如换成 options 结构体），
  跟着上游的新签名走，把 `departmentID`/`username`/`displayName` 作为新字段塞进去，不要保留旧签名。
- `model/user.go` 里 `User` 结构体的对齐格式差异：只保留字段新增，不要为消掉格式 diff 去重排上游字段。

### 验证

```bash
fork/scripts/verify.sh
```

在本机没有 Go 的情况下也能跑——脚本把仓库挂进 `golang:1.26.10-alpine` 容器执行：`go build`
（controller/model/service/middleware/router）、`go vet`、`gofmt -l`，以及
`go test ./model/ ./controller/ ./service/`。

两个已知限制：

- **不构建根包。** `main.go` 有 `//go:embed web/dist`，而 `web/dist` 在 `.gitignore` 里、只在
  Dockerfile 第一阶段构建前端后才存在。裸 `go build ./...` 必然失败，属预期。
- **必须在 Docker 可挂载的路径下执行。** macOS 上临时目录通常不在 Docker Desktop / colima 的
  共享列表里，挂载后 `/src` 是空的。在正常仓库路径下执行即可；worktree 在
  `.claude/worktrees/` 下，同样可以。

实测输出：

```
==> build
==> vet
==> gofmt
==> test
ok  	github.com/QuantumNous/new-api/model	11.285s
ok  	github.com/QuantumNous/new-api/controller	53.941s
ok  	github.com/QuantumNous/new-api/service	13.864s
verify passed
```

仅编译通过不算升级成功——下面「测试覆盖不到」那四项仍需人工验证。

### 并发用例（可选，需要 Postgres）

`TestConcurrentRevokeLeavesOneEnabledRoot` 在默认校验里 FAIL/SKIP（未设 `TEST_POSTGRES_DSN`），
因此最核心的不变量「至少留一个启用的 root」缺少自动化覆盖。要真跑它：

```bash
docker run --rm \
  -v /绝对路径/new-api:/src -v tm-gomod:/go/pkg/mod -w /src \
  -e GOWORK=off -e GOPROXY=https://goproxy.cn,direct \
  -e TEST_POSTGRES_DSN='postgres://user:pass@host:5432/newapi_test?sslmode=disable' \
  golang:1.26.10-alpine \
  go test ./model/ -run TestConcurrentRevokeLeavesOneEnabledRoot -count=1 -v
```

> **DSN 必须指向独占的测试库，且库名必须含 `test`。** 该用例会 `DROP TABLE users` 再重建；
> 库名不含 `test` 时直接 FAIL 而不是照跑，防止误连共享实例清空账号。用例内部还会临时把主库
> 类型切换为 PostgreSQL，否则 `lockForUpdate` 在 SQLite 下不会发 `FOR UPDATE`。

### 构建镜像

```bash
fork/scripts/build.sh                              # 默认 tag：rc.49-tm3
fork/scripts/build.sh /path/to/new-api my-tag-1    # 自定义源码路径与 tag
```

构建时间包含一次 `bun install` 加前端构建（Dockerfile 第一阶段），不能跳过。

> **tag 约定见 `token-master2/deploy/.env.example`**：`tm1`=多 root，`tm2`=加最近调用，
> `tm3`=加部门。源码里三组定制恒在，所以默认 tag 是 `rc.49-tm3`。**不要打 `tm1`/`tm2`**——
> 源码不会退回中间状态，打小号 tag 只会让部署侧误判，跑起来部门列、部门筛选与聚合接口整块 404。

国内网络通常拉不到 registry 与 Go proxy，用环境变量显式传代理，**不要**指望 shell 里的
`HTTPS_PROXY` 自动生效：

```bash
TM_BUILD_PROXY=http://<容器可达的宿主机 IP>:<端口> fork/scripts/build.sh
```

宿主机上的 `127.0.0.1:xxxx` 进了容器指向容器自身，传进去等于没配。要填**容器可达**的地址
（colima 下一般是宿主内网 IP）。

> **tag 里的 `rc.49` 只是沿用部署侧命名习惯，不代表源码基线是上游 `v1.0.0-rc.49`。**
> 上线前必须跑完整回归。本 fork 的基线提交是 `6ef123cd5`。

## 新增接口能力

- `POST /api/user/manage` 的 `action` 多了 `grant_root`、`revoke_root`。请求体形如
  `{"id": <目标用户 id>, "action": "grant_root"}`，需带 `X-Security-Proof`
  （scope `admin.user.manage`，context `{"user_id", "action"}`）。
- `GET /api/user/`、`GET /api/user/search`、`GET /api/user/:id` 的响应里 `User` 多了
  `last_called_at`（秒级 Unix 时间，`0` 表示从未调用）与 `department_id`（`null` 表示未分配）。
- `GET /api/user/search` 多了三个精确筛选参数：`department_id`、`username`、`display_name`。
  非法 `department_id` 返回 400 而不是静默忽略。
- `POST /api/user/` 接受 `department_id`，并在响应 `data.id` 里回传新用户 id。
- `GET|POST|PUT /api/department/`、`DELETE /api/department/:id`：部门字典读写。
- `PUT /api/user/:id/department`：改单个用户的部门归属，请求体 `{"department_id": <id>}`，
  传 `null` 表示清空。
- `GET /api/data/departments?start_timestamp=&end_timestamp=`：部门用量聚合，
  返回 `members`/`keys`/`calls`/`tokens`/`quota`。

调用约定：`grant_root` 只有 root 能调，`revoke_root` 受「至少留一个启用 root」约束；两者都要求
目标用户存在且状态合法。部门的写操作与改归属都受 `canManageTargetRole` 约束（同级或更高角色的
账号不可操作）。

## 测试覆盖不到的四点

自动化测试覆盖了不变量、写路径与接口行为，但以下四项只能人工验证：

1. **`revoke_root` 的 Casbin override 清理**：现有测试无法区分「走了 `demote` 分支」与「走了
   `else` 分支」，两条都会把 role 写成 1。唯一手段是在 Postgres 上直接查 `casbin_rule`
   （目标用户先 `SetUserPermissions`，撤销后应无 override）。
2. **`last_called_at` 的端到端可见性**：需要真实扣费后查库，确认时间戳是秒级真实时刻，
   且管理员充值、退款不会刷新它。
3. **部门用量聚合的数字正确性**：测试用的是造出来的 `quota_data`/`tokens` 行，能验证 CTE 结构与
   零成员部门不丢失，但真实库里「一个用户多把秘钥」「跨部门迁移后历史流水仍记在旧账上」等
   形态要拿生产数据抽样核对。特别地，`keys` 只统计成员**当前**拥有的有效秘钥数。
4. **前端渲染**：本仓库前端未改动，若在自己前端展示这些字段/接口，属另一侧的验证。

## 已知限制（有意不修）

| 现象 | 说明 |
|---|---|
| root 之间可互相重置密码、互相禁用、互相撤销 root | step-up 只证明「操作者是某个 root」，不区分是哪一个。这是多 root 的固有代价，不是缺陷。全部动作进上游审计。 |
| `grant_root` 不吊销目标会话 | 与上游升权一致（上游只在降级时吊销）。降权会即时生效，因为鉴权读取实时角色。 |
| 渠道测试固定取「第一个 root」当测试用户 | 多 root 下取到哪个不确定，行为可接受。 |
| 渠道通知默认发给所有未显式配置的 root | 多 root 后收件人变多，行为可接受。 |
| `CountEnabledRootUsers` 生产代码里没有调用点 | 守卫走的是加锁路径。该函数留给排查与测试用。 |
| 新建用户的默认侧边栏配置不含 admin 区，事后 `grant_root` 不重算 | 只影响上游自带控制台，本仓库未改前端。 |
| 极端情况下仍可能被人工操作清空 root（手工 SQL、迁移脚本） | 事故兜底：把可信账号 `UPDATE users SET role=100, status=1`，或删除 setups 记录后重启走初始化。**仅事故时人工执行，不可当控制手段。** |

## 许可证与合规

上游是 **AGPLv3**（`LICENSE`），并启用了 §7 附加条款（`NOTICE`）：修改版必须保留署名
`Frontend design and development by New API contributors.`，并在界面显著位置保留指向
<https://github.com/QuantumNous/new-api> 的可见链接。本 fork 未改动这些标识。

AGPLv3 的网络条款意味着：通过网络向用户提供服务时，有义务提供**修改后的完整源码**。因此
本 fork 不能私有，必须可获取。若不愿承担该义务，上游的商业授权联系人是 `support@quantumnous.com`。

**状态：需法务确认，上线前必须落定。**
