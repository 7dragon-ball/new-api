# fork/ — New API 的定制补丁

本仓库是 [New API](https://github.com/QuantumNous/new-api)（AI API 网关）的 fork，在上面做了三组
后端定制：支持多个超级管理员（root）、在 `users` 表记录真实的最近调用时刻，以及把部门归属
从业务库搬到 `new_api` 库。

定制不是直接改上游代码，而是以 git 补丁的形式存于本目录 `fork/patches/`，由 `fork/scripts/apply.sh`
落到源码树。所以 `fork/patches/` 是定制的**权威清单**：从一个干净的上游提交出发、用 `apply.sh`
即可重建出完全相同的定制状态。

本文件说明补丁做了什么、怎么用、以及升级时要注意什么。这些定制没有别的说明文档——`docs/` 里
的文档都是上游自带的，与本目录的改动无关，需要的信息都在这里。

## 本仓库 main 分支的代码状态（动手前必看）

补丁清单是 15 个，但 **`main` 的源码树里只应用了 0008-0009**：

| 范围 | 在 `main` 源码树里 | 说明 |
|---|---|---|
| 0001-0007 多 root | **仅存于 `fork/patches/`** | 没有对应的代码提交。`model/user.go` 里看不到 `ensureEnabledRootSurvivesWithTx` 等函数。 |
| 0008-0009 最近调用时刻 | **已应用** | `users.last_called_at` 字段与三条写入路径都在代码里。 |
| 0010-0015 部门归属 | **仅存于 `fork/patches/`** | 没有对应的代码提交。`model/department.go` 在 `main` 上不存在。 |

这个不对称不影响用法，但会影响判断：在 `main` 上 `grep` 不到多 root 或部门的代码，不代表补丁丢了。

因此 **不要在 `main` 上直接跑 `apply.sh`**——0008/0009 会因目标改动已存在而冲突。
要得到「15 个补丁全部应用」的完整状态，必须从一个不含 0008/0009 的干净上游提交出发：

```bash
git checkout -b fork-full origin/main    # 或任意上游提交
fork/scripts/apply.sh                    # 15 个补丁逐个 ok
```

实测：从导出时的 `origin/main`（本组补丁的基线）出发，15 个补丁全部干净重放，
重放后的源码树与本分支零差异，`verify.sh` 全绿。基线会随上游前进，因此这里不记具体提交号。

## 为什么用补丁，而不是直接改代码

上游更新频繁，直接改代码会让每次升级都变成手工合并。补丁方式带来三个好处：

- **升级路径确定**：`git am --3way` 逐块应用，冲突位置明确，只需重落冲突块。
- **可回退**：补丁应用后是本地提交，`git reset` 即回到干净的上游代码。
- **可审计**：补丁本身就是改动清单，配合 `// [TM-FORK]` 标记可精确定位每处与上游不同的地方。

代价是：补丁记录了导出时的上游代码上下文。上游前进后旧补丁可能无法直接应用，需要解决冲突后重新导出。

## 三组补丁分别解决什么

### 一、多 root 管理员（0001-0007）

**上游行为**：`role=100`（root / 超级管理员）在系统里唯一——控制器里有多处硬编码把「root 唯一」写死，
没有任何接口能产出或撤销第二个 `role=100` 账号。

**需求**：管理员可以有多个。产品语义里「管理员」就是 `role=100`，因此需要放开唯一性。

**做法**：不删掉唯一性假设（那样会失去兜底），而是把它换成一个正确的不变量：

> 任何把某个「`role=100` 且状态启用且未软删除」的账号改成非 root、改成禁用、或删除的操作，
> 都要求此刻启用的 root 数 > 1。

同时在控制器上做最小放开：允许 root 创建 root、允许把普通用户提为 root / 撤销 root。

关键设计：守卫在事务内、在任何写之前执行，并锁住整组启用中的 root 行
（`SELECT ... FOR UPDATE`，按 id 排序以避免死锁），而不是只做一次 `COUNT`。两个并发请求各自
只读计数会双双通过；加行锁后第二个事务会阻塞到第一个提交，随后读到 1 并被拒绝。

补丁按「先建守卫、再开门」排序，顺序不可调换：

1. 0001 先加辅助函数，此时没有任何生产路径调用它，行为零变化。
2. 0002-0003 把守卫挂进更新与删除路径。
3. 0004-0006 才允许产出/撤销第二个 root。
4. 0007 补并发用例。

只放开上游语义之外的部分：`role=10`（普通管理员）的 `promote`/`demote` 保持上游行为不变，
多 root 走新增的 `grant_root`/`revoke_root` 两个 action，属纯增量，回滚只需删这两个 case。

### 二、最近调用时刻（0008-0009）

**上游行为**：`quota_data.created_at` 在写入时就被抹成整点（`created_at - created_at % 3600`），
所以按用量流水推算「最近一次调用」只能得到 `xx:00`，无法显示真实时刻。`users.last_login_at`
记录的是登录时间，不是调用时间。

**做法**：在 `users` 表新增 `last_called_at`，在已经发生的扣费 UPDATE 里同行写入秒级时间戳，
不增加数据库往返。

**难点是区分「调用」与「非调用」**。同一个 UPDATE 落点承担三类写入：真实计费、管理员充值/兑换码、
任务退款结算。只有第一类算调用。判定方式是看 `request_count`：一次调用必然伴随一次请求数累加，
退款是相反的减法，充值不动请求数。因此：

- 非批量路径：`updateUserUsedQuotaAndRequestCount` 直接写时间戳。
- 批量路径：`updateUserQuotaUsedQuotaAndRequestCount` 仅在 `requestCount > 0` 时写。
- 退款路径：`UpdateUserUsedQuota` 只被退款/结算调用，保持不动。
- 0009：`UpdateWithTx` 的整行覆盖会把调用方读到的旧值写回，`Omit` 白名单必须排除该字段。

结果：`users.last_called_at` 是真实扣费时刻；用户列表接口（`GET /api/user/`、`/api/user/search`）
随 `User` 结构体一起返回，字段为 `last_called_at`（秒级 Unix 时间，`0` 表示从未调用）。

> **本仓库前端未改动。** `web/` 与上游完全一致，自带控制台不显示这一列。该字段供自行开发的
> 管理端读取；要用的话在列表里渲染 `last_called_at` 即可，无需额外接口。

### 三、部门归属（0010-0015）

**背景**：部门原本存在业务库 `token_master` 的 `department` 表里，与账号数据分离在两个库，
任何「按部门筛选用户」「按部门聚合用量」都要跨库拼装，BFF 侧只能整表拉回内存再过滤。

**做法**：把部门字典搬进 `new_api` 库（`department` 表 + `users.department_id`），让归属成为
账号自己的属性，筛选与聚合都能在一条 SQL 里完成。业务库只保留手机号等附加资料。

- 0010：`model/department.go`（`department` 表、名字归一与 1-30 字校验、`uk_department_name`
  唯一索引）与 `users.department_id`（可空、带索引）。**有意不加外键**：外键的 RESTRICT 看不见
  `deleted_at`，被软删除的用户仍会挡住部门删除，所以「还有几个用户」这条检查由
  `DeleteDepartment` 自己数未软删的行。GORM 零值跳过会让可空 FK 写不进 NULL，字段声明成 `*int`。
- 0011：`/api/department/` 的增删改查（`GET` 支持 `keyword`，`LIKE` 里的 `%`/`_` 已转义）。
- 0012：`GET /api/user/search` 改为原生 SQL 过滤，支持按部门与用户名；软删除范围收紧；
  `POST /api/user/` 接受 `department_id` 并在响应里回 `data.id`。
- 0013：新增 `PUT /api/user/:id/department`。**不复用 `PUT /api/user/`**——那是上游自带前端
  在用的整行替换接口，请求体不带部门时会被整行覆盖清空。
- 0014：`GET /api/data/departments` 按时间区间聚合各部门的成员数、秘钥数、调用数、token 数与
  额度。三个数据源各自聚合成 CTE 再 LEFT JOIN：摊平成多表 JOIN 会做
  `users × tokens × quota_data` 笛卡尔积，有 N 把秘钥的用户用量被放大 N 倍。
- 0015：`UpdateUserDepartment` 补 `canManageTargetRole`（否则 `role=10` 能改 `role=100` 的部门，
  而它连读该用户都会被拒）；审计参数传解引用值而不是 `*int`（`%v` 渲染指针会写成 `0xc0004a21b0`）。

**发布顺序**：必须先部署含这些接口的新版 BFF，再执行删掉 `token_master.department` 与
`user_profile.department_id` 的 goose 迁移。旧版 BFF 仍在读写那两列，列一删就立刻报错。

## 目录结构

```
fork/
├── README.md
├── patches/          # git format-patch 产物，每个提交一个补丁
└── scripts/
    ├── apply.sh      # 把 patches/ 应用到源码树
    ├── verify.sh     # 编译 + vet + gofmt + 子包测试（容器内）
    └── build.sh      # docker build 出镜像
```

## 补丁清单

| # | 内容 |
|---|---|
| 0001 | 新增 `ErrLastEnabledRootUser` 与三个辅助函数：`CountEnabledRootUsers`（仅诊断/测试用）、`lockEnabledRootIDsWithTx`（按 id 排序加行锁）、`ensureEnabledRootSurvivesWithTx`（唯一判定入口）。此刻无生产路径调用，行为零变化。 |
| 0002 | 守卫挂进 `UpdateWithTx`（降级 / 禁用路径）。外层先用读到的 `current.Role` 做廉价预筛，真正判定在加锁后的数据上重做。 |
| 0003 | 守卫挂进 `delete`：管理员删除路径受不变量约束；自删路径（`DELETE /api/user/self`）保持上游语义，root 删自己仍返回 `ErrCannotDeleteRootUser`（放开会与「至少留一个」形成两套重叠语义）。删除一个**已禁用**的 root 不受限，因为它不减少可用管理员数。 |
| 0004 | `CreateUser` 的角色检查加 `myRole != RoleRootUser` 豁免：root 可创建同级账号，不改变其他角色的限制（仍不得创建同级或更高）。 |
| 0005 | `service/security_verification.go` 的 `adminUserManageActions` 白名单加 `grant_root`/`revoke_root`。不登记则 proof 绑不上（`ErrVerificationContextInvalid`），后续接口必然 403。 |
| 0006 | `ManageUser`：删掉 disable/delete 对 root 的两处硬拒（保护改由模型层不变量承担），新增 `grant_root` / `revoke_root`。`grant_root` 保留调用者必须是 root 的检查——`role=10` 的账号在其余链路上全部放行（`canManageTargetRole(10,1)` 为真、proof 只要求 `state.Role >= RoleAdminUser`），这是唯一一道闸。`revoke_root` 必须走 `demote` 分支，因为该分支负责清理目标用户的 Casbin 逐用户 override：root 是 superuser 时 override 无意义，降级后这些旧 override 会立刻变成真实权限。 |
| 0007 | Postgres 并发用例：两个事务同时把两个 root 各自降级，断言恰好一个成功、一个被拒，且最终只剩一个启用的 root。SQLite 无 `FOR UPDATE`，跑不出锁竞争，因此单独开 Postgres 连接。 |
| 0008 | `User` 新增 `LastCalledAt`（`users.last_called_at`，默认 0）；三条扣费 UPDATE 路径同行写入秒级时间戳（含批量刷库落点）。该补丁同时带进 `model/user.go` 结构体的整段对齐格式化 diff，属噪声，不影响行为。 |
| 0009 | `UpdateWithTx` 的 `Omit` 白名单加 `last_called_at`，防止整行覆盖写回旧值。 |
| 0010 | 新增 `model/department.go`：`department` 表（显式 `TableName` 单数，手写 SQL 都写 `department`）、名字归一（全角空格→半角后 TrimSpace）与 1-30 字校验、`uk_department_name` 唯一索引。`User` 新增 `DepartmentId *int`（`users.department_id`）：可空 FK 必须是 `*int`，否则 GORM 的零值跳过让「清空部门」写不进 NULL。 |
| 0011 | `/api/department/` 的 `GET`（`keyword` 模糊查，`%`/`_`/`\` 已转义并带 `ESCAPE`）、`POST`、`PUT`（id 走请求体，与上游 `prefill_group` 同形）、`DELETE /:id`。全部挂 `AdminAuth`，写操作进管理审计。重名靠唯一索引拦，错误转成 `Department name already exists`。 |
| 0012 | `GET /api/user/search` 换成原生 SQL：支持 `department_id` 与用户名过滤、管理员可查已软删除账号（显式 WHERE 而非 `Unscoped()` 裸放开）；`POST /api/user/` 接受 `department_id` 并在响应 `data.id` 里回传新 id（BFF 建号后要立刻写手机号，拿不到 id 就只能全表扫）。 |
| 0013 | 新增 `PUT /api/user/:id/department`，请求体 `{"department_id": <id>}`，传 `null` 表示清空。独立路由而非复用 `PUT /api/user/`：后者是整行替换，上游自带前端调用时不带部门字段，复用会把部门冲掉。 |
| 0014 | 新增 `GET /api/data/departments?start_timestamp=&end_timestamp=`，按时间区间聚合各部门 `members`/`keys`/`calls`/`tokens`/`quota`。三个数据源各聚成 CTE 再 LEFT JOIN（`dept_users`/`tk`/`qd`）：摊平成一条多表 JOIN 会做 `users × tokens × quota_data` 笛卡尔积，把有 N 把秘钥的用户的调用量放大 N 倍，`count(DISTINCT)` 只掩盖症状。全 LEFT JOIN 让零成员部门也出现。 |
| 0015 | 补两处终审问题：`UpdateUserDepartment` 加 `canManageTargetRole` 分级校验（与 `UpdateUser`/`ManageUser`/`DeleteUser` 同一条边界，缺了它 `role=10` 能改 `role=100` 的部门）；审计参数改传解引用值而非 `*int`（`auditContentEN` 用 `%v` 渲染，传指针会把 `0xc0004a21b0` 写进审计 Content，清空时写 `<nil>`）。 |

代码改动总量（不含本目录）。多 root + 最近调用（0001-0009）：

```
 controller/security_enrollment_test.go |   3 +
 controller/user.go                     |  46 ++++--
 controller/user_multi_root_test.go     | 249 +++++++++++++++++++++++
 model/user.go                          | 195 +++++++++++++++------
 model/user_last_called_test.go         | 126 ++++++++++++++
 model/user_multi_root_test.go          | 264 ++++++++++++++++++++++++++
 service/security_verification.go       |   4 +-
 7 files changed, 829 insertions(+), 58 deletions(-)
```

部门（0010-0015）：

```
 controller/audit.go                       |   6 +
 controller/department.go                  | 126 +++++++++++++++++++
 controller/department_test.go             | 195 ++++++++++++++++++++++++++++++
 controller/department_usage.go            |  69 +++++++++++
 controller/department_usage_test.go       | 169 ++++++++++++++++++++++++++
 controller/security_enrollment_test.go    |   2 +-
 controller/user.go                        |  88 +++++++++++++-
 controller/user_department_test.go        | 188 ++++++++++++++++++++++++++++
 controller/user_search_department_test.go | 133 ++++++++++++++++++++++++++++
 model/department.go                       |  36 ++++++
 model/department_test.go                  |  62 ++++++++++
 model/main.go                             |   1 +
 model/task_cas_test.go                    |   1 +
 model/user.go                             |  27 ++++-
 model/user_department_test.go             |  66 ++++++++++
 model/user_pagination_test.go             |   2 +-
 model/user_search_filter_test.go          |  97 +++++++++++++++
 router/api-router.go                      |  14 +++
 18 files changed, 1275 insertions(+), 7 deletions(-)
```

测试：多 root + 最近调用共 25 个用例（model 17、controller 8）；部门相关新增 44 个 `Test` 函数
（model 与 controller 两包）。上游受保护的项目标识（README、LICENSE、NOTICE、`web/`、Dockerfile、
Go module path）一律未改动；`web/` 与上游完全一致。

## 改了哪些文件

| 文件 | 改动 |
|---|---|
| `model/user.go` | 不变量辅助函数、`UpdateWithTx`/`delete` 的守卫、`LastCalledAt` 字段与三条扣费写入路径、`DepartmentId` 字段、`/api/user/search` 的过滤 SQL |
| `model/department.go` | 新增：部门字典、名字归一与校验、`BeforeSave` |
| `controller/user.go` | `CreateUser` 允许 root 建 root；`ManageUser` 删两处硬拒、新增两个 action、扩写 demote 分支条件；`SearchUsers` 部门/名字过滤；新增 `UpdateUserDepartment` |
| `controller/department.go` | 新增：部门增删改查 |
| `controller/department_usage.go` | 新增：部门用量聚合 |
| `controller/audit.go` | 新增 `department.*` 审计 action 的英文模板 |
| `router/api-router.go` | 新增 `/api/department/` 四路由、`PUT /api/user/:id/department`、`GET /api/data/departments` |
| `service/security_verification.go` | proof action 白名单加两项 |
| `controller/security_enrollment_test.go` | 绑定清单加用例 |
| `model/user_multi_root_test.go` | 新增：不变量、锁行为、自删语义、并发 |
| `model/user_last_called_test.go` | 新增：三条写入路径 + 纯额度变更/退款不计入 + 编辑不覆盖 |
| `model/department_test.go`、`model/user_department_test.go`、`model/user_search_filter_test.go` | 新增：字典 CRUD、空值语义、筛选 SQL |
| `controller/user_multi_root_test.go` | 新增：建号、授权/撤销、最后一个 root 保护、缺少 proof、`role=10` 提权被拒 |
| `controller/department_test.go`、`controller/user_department_test.go`、`controller/user_search_department_test.go`、`controller/department_usage_test.go` | 新增：部门接口、改归属（含越权与审计）、筛选、聚合 |

新增接口能力：

- `POST /api/user/manage`（请求体 `ManageRequest`）的 `action` 多了 `grant_root`、`revoke_root`。
  请求体形如 `{"id": <目标用户 id>, "action": "grant_root"}`，需带 `X-Security-Proof`
  （scope `admin.user.manage`，context `{"user_id", "action"}`）。
- `GET /api/user/`、`GET /api/user/search`、`GET /api/user/:id` 的响应里 `User` 多了
  `last_called_at`（秒级 Unix 时间，`0` 表示从未调用）与 `department_id`（`null` 表示未分配）。
- `GET|POST|PUT /api/department/`、`DELETE /api/department/:id`：部门字典读写。
- `PUT /api/user/:id/department`：改单个用户的部门归属。
- `GET /api/data/departments`：部门用量聚合，`members`/`keys`/`calls`/`tokens`/`quota`。

调用约定：`grant_root` 只有 root 能调，`revoke_root` 受「至少留一个启用 root」约束；两者都要求
目标用户存在且状态合法。部门的写操作与改归属都受 `canManageTargetRole` 约束（同级或更高角色的
账号不可操作）。

## 用法

三个脚本的 `UPSTREAM` 默认值都由脚本自身位置推导（`$(dirname "$0")/../..`），从任何 cwd 调用
都能定位到仓库根，也可以显式传路径覆盖。以下命令均在仓库根执行。

### 应用补丁

**必须从一个不含 0008/0009 的干净上游提交出发**（原因见上文「main 分支的代码状态」）：

```bash
git status --short            # 工作区必须是干净的
git checkout -b fork-full origin/main
fork/scripts/apply.sh
```

全部 15 个补丁按文件名顺序 `git am --3way` 应用，成功时逐个打印 `ok`，最后以 `done.` 结束。

> **只能应用一次。** 补丁已应用后再跑会失败并进入 `git am` 冲突态（脚本报
> `conflict or already applied`）——因为补丁要新建的文件已在工作区。判断是否已应用请看历史：
>
> ```bash
> git log --oneline -15         # 已应用则应看到 15 条补丁提交
> git am --abort                # 若已误触发冲突态，用这个退出
> ```
>
> 同理，在已含 0008/0009 的 `main` 上跑：0001-0007 成功，0008 报
> `conflict or already applied` 并停下。脚本对每个补丁单独起一次 `git am`，
> 所以 `git am --abort` 只会退回 0008 这一轮、把工作区留在 0007 之上；
> 要彻底回干净，再 `git reset --hard origin/main`（**只在专为试跑新建的分支上
> 这么做**，别在 `main` 上重置）。

冲突时按 `git status` 提示逐块解决，然后 `git am --continue`。

### 校验

```bash
fork/scripts/verify.sh
```

在本机没有 Go 的情况下也能跑——脚本把仓库挂进 `golang:1.26.10-alpine` 容器执行：
`go build`（controller/model/service/middleware/router）、`go vet`、`gofmt -l`，
以及 `go test ./model/ ./controller/ ./service/`。

两个已知限制：

- **不构建根包。** `main.go` 有 `//go:embed web/dist`，而 `web/dist` 在 `.gitignore` 里、只在
  Dockerfile 第一阶段构建前端后才存在。裸 `go build ./...` 必然失败，属预期。
- **必须在 Docker 可挂载的路径下执行。** macOS 上临时目录（如 `$TMPDIR` 下）通常不在
  Docker Desktop / colima 的共享列表里，挂载后 `/src` 是空的，报错类似
  `directory prefix controller does not contain main module`。在正常仓库路径下执行即可。

实测输出（全部通过；耗时为示例，随机器而变）：

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

### 并发用例（可选，需要 Postgres）

`0007` 的并发用例在默认校验里是 SKIP（未设 `TEST_POSTGRES_DSN`），因此最核心的不变量
「至少留一个启用的 root」缺少自动化覆盖。要真跑它：

```bash
docker run --rm -v "$PWD":/src -v tm-gomod:/go/pkg/mod -w /src \
  -e GOWORK=off -e GOPROXY=https://goproxy.cn,direct \
  -e TEST_POSTGRES_DSN='postgres://user:pass@host:5432/newapi_test?sslmode=disable' \
  golang:1.26.10-alpine \
  go test ./model/ -run TestConcurrentRevokeLeavesOneEnabledRoot -count=1 -v
```

> **DSN 必须指向独占的测试库，且库名必须含 `test`。** 该用例会 `DROP TABLE users` 再重建；
> 库名不含 `test` 时直接 FAIL 而不是照跑，防止误连共享实例清空账号。用例内部还会临时把
> 主库类型切换为 PostgreSQL，否则 `lockForUpdate` 在 SQLite 下不会发 `FOR UPDATE`。

### 构建镜像

```bash
fork/scripts/build.sh                              # 默认 tag：rc.49-tm1
fork/scripts/build.sh /path/to/new-api my-tag-1    # 自定义源码路径与 tag
```

镜像名前缀与默认 tag 是脚本内置值，按自己的部署习惯覆盖即可。构建时间包含一次 `bun install`
加前端构建（Dockerfile 第一阶段），不能跳过。

> **默认 tag `rc.49-tm1` 已落后于补丁集，部署时请显式传 tag。** 三组补丁对应的 tag 约定见
> `token-master2/deploy/.env.example`：`tm1`=多 root，`tm2`=加最近调用，`tm3`=加部门。
> 当前 15 个补丁全部应用后的产物应打成 `rc.49-tm3` 这类名字；跑 `tm1`/`tm2` 镜像会让部门列、
> 部门筛选与聚合接口整块 404。

构建期要从镜像仓库拉基础镜像、从 Go proxy 拉模块，国内网络通常不通。用环境变量显式传代理，
**不要**指望 shell 里的 `HTTPS_PROXY` 自动生效：

```bash
TM_BUILD_PROXY=http://<容器可达的宿主机 IP>:<端口> fork/scripts/build.sh
```

宿主机上的 `127.0.0.1:xxxx` 进了容器指向容器自身，传进去等于没配，且报错很难懂。要填
**容器可达**的地址（colima 下一般是宿主内网 IP）。

> **默认 tag 里的 `rc.49` 只是沿用部署侧命名习惯，不代表源码基线是上游 `v1.0.0-rc.49`。**
> 上线前必须跑完整回归，不能假设与某个上游 release 等价。

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

## 标记约定

每处改动或紧邻的一组改动上方（只标补丁块，不给整个函数加标记）：

```go
// [TM-FORK] <一句话说明为什么这块和上游不同>
```

9 个补丁全部应用后共 24 处、5 个文件。加上部门补丁（0010-0015）后，15 个补丁共 46 处、10 个文件。
在只含 0008-0009 的 `main` 上只能 grep 到 10 处
（`model/user.go` 4 处、`model/user_last_called_test.go` 6 处），这是正常的——多 root 与部门的
标记只在补丁里。完整清单：

```bash
grep -rn "\[TM-FORK\]" --include="*.go" .
```

> 如果仓库里还有其他定制补丁（例如后续追加的补丁组），这条命令会把它们一并列出，
> 数量随之变化。

## 上游升级

```bash
git fetch origin
git checkout -b upgrade/<new-commit>
fork/scripts/apply.sh            # git am --3way
fork/scripts/verify.sh
```

补丁应用后按需重新导出（`-o` 必须在范围参数之前，写在后面源码树里的旧补丁不会被覆盖）：

```bash
git format-patch -o fork/patches <new-baseline>..HEAD -- . ':!fork'
```

> 这条命令用范围导出，产物 subject 带 `[PATCH n/m]` 前缀；而本目录现有补丁是逐个提交
> `git format-patch --no-signature --stdout -1 <sha>` 导出的，subject 是 plain `[PATCH]`。
> 重导出后请统一成一种，否则同一目录里两种风格并存。

冲突优先级（按概率降序）：`ManageUser` 的 switch → `UpdateWithTx`/`delete` → 白名单 →
`CreateUser` 的等级检查 → `SearchUsers`/`GetAllUsers`（部门筛选改了 SQL）→ `router/api-router.go`
（三组补丁都在同一处插路由）。

处理原则：

- 先接受上游版本，再按 `// [TM-FORK]` 逐块重落补丁。
- 守卫**永远**在事务内、永远在写之前，不因上游重构挪到控制器。
- `adminUserManageActions` 是白名单，上游新增 action 时**取并集**。
- 部门相关的手写 SQL（删除计数、用量聚合）都写 `department`（单数），`Department.TableName()`
  必须保持单数，否则上游若改动默认命名策略会静默错表。
- 升级后重跑 `verify.sh`，并人工验证上面「测试覆盖不到」的四项。仅编译通过不算成功。

## 已知限制（有意不修）

| 现象 | 说明 |
|---|---|
| root 之间可互相重置密码、互相禁用、互相撤销 root | step-up 只证明「操作者是某个 root」，不区分是哪一个。这是多 root 的固有代价，不是缺陷。全部动作进上游审计，操作者/目标/验证方式可追溯。 |
| `grant_root` 不吊销目标会话 | 与上游升权一致（上游只在降级时吊销）。降权会即时生效，因为鉴权读取实时角色。 |
| 渠道测试固定取「第一个 root」当测试用户 | 多 root 下取到哪个不确定，行为可接受。 |
| 渠道通知默认发给所有未显式配置的 root | 多 root 后收件人变多，行为可接受。 |
| `CountEnabledRootUsers` 生产代码里没有调用点 | 守卫走的是加锁路径。该函数留给排查与测试用，不要误以为守卫依赖它。 |
| 新建用户的默认侧边栏配置不含 admin 区，事后 `grant_root` 不重算 | 只影响上游自带控制台，本仓库未改前端。 |
| 极端情况下仍可能被人工操作清空 root（手工 SQL、迁移脚本） | 事故兜底：把可信账号 `UPDATE users SET role=100, status=1`，或删除 setups 记录后重启走初始化。**仅事故时人工执行，不可当控制手段。** |

## 许可证与合规

上游是 **AGPLv3**（`LICENSE`），并启用了 §7 附加条款（`NOTICE`）：修改版必须保留署名
`Frontend design and development by New API contributors.`，并在界面显著位置保留指向
<https://github.com/QuantumNous/new-api> 的可见链接。本组补丁未改动这些标识。

AGPLv3 的网络条款意味着：通过网络向用户提供服务时，有义务提供**修改后的完整源码**。因此
本 fork 不能私有，必须可获取。若不愿承担该义务，上游的商业授权联系人是 `support@quantumnous.com`。

**状态：需法务确认，上线前必须落定。**
