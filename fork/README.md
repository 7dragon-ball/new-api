# fork/ — New API 的定制登记册

本仓库是 [New API](https://github.com/QuantumNous/new-api)（AI API 网关）的 fork，在上面做了四组
后端定制：支持多个超级管理员（root）、在 `users` 表记录真实的最近调用时刻、把部门归属从业务库
搬到 `new_api` 库、把手机号也搬进 `new_api.users`。

**定制直接写在源码里，以 `// [FORK]` 标记。** 没有补丁目录，也没有 `git am` 重放步骤。
本文件是唯一的定制登记册：哪几处与上游不同、为什么不同、merge 上游时怎么保住它们。

## 为什么改成直接改源码

早期的做法是 `git format-patch` 导出补丁、升级时 `git am --3way` 重放。它有三个实际代价：

- **补丁与源码树天然会漂**。补丁导出后源码又改过，补丁就再也应用不上。本仓库曾同时存在
  「补丁清单说 15 个」和「源码树只应用了 2 个」的错位状态，读文档的人无法判断真实代码。
- **重放失败要人工分辨「已应用」还是「真冲突」**。`git am` 对已应用的补丁报 `conflict or
  already applied`，与真冲突同一种报错，误判一次就要 `git am --abort` 收拾残局。
- **上游前进后旧补丁整体失效**。补丁记的是导出时的上游上下文，上游一重构，全部补丁同时变红，
  而不是只有真正冲突的那几块变红。

直接改源码之后，merge 时 git 的合并算法按行处理，只有真正重叠的行才冲突。

代价是 `// [FORK]` 标记成了唯一线索，因此每处改动都必须在标记的上一行说清「为什么和上游不同」。

## 仓库布局

```
fork/
└── README.md          # 本文件：定制登记册 + merge 流程
```

构建与验证都不需要额外脚本，用仓库自带的 `Dockerfile`、`Makefile` 即可，见「验证」与「构建镜像」。

上游自带的 `docs/` 与本目录无关，需要的信息都在这里。

### 新增文件放在哪

**新增文件留在它所属的包里，文件名加 `_fork` 后缀**（测试文件是 `xxx_fork_test.go`，
因为 Go 要求 `_test.go` 必须在末尾）：

```
controller/department_fork.go            # 不是 controller/fork/department.go
controller/department_fork_test.go
model/department_fork.go
model/goose_fork.go
```

不建 `controller/fork/` 子目录，原因是 **Go 的包边界是目录**：子目录里的文件即使写
`package controller`，也是另一个包，拿不到 `recordManageAudit`、`parseFlowQuotaTimeRange`
这些上游未导出的符号；包内测试也无法跟随。`_fork` 后缀让文件留在同包，`ls` 一眼可辨，
且对编译、测试、merge 零影响。

改**上游已有文件**的部分仍然靠 `// [FORK]` 标记（见「标记约定」）。

### 数据库迁移（goose）

**PostgreSQL 上需要 AutoMigrate 之外的 schema 变更统一走内嵌 goose 迁移**，当前第一份是
`department` 表；其余表仍由 `migrateDB()` 的 AutoMigrate 建。两者在同一时点执行：
`migrateDB()` 先跑 goose，再跑 AutoMigrate。

```
migrations/embed.go                      # //go:embed + 暴露 migrations.Postgres
migrations/postgres/*.sql                # 只有 PG 一份，收录该目录下全部迁移
model/goose_fork.go                      # goose.NewProvider + Up()，消费上面的 FS
```

`runForkMigrations()` 按方言目录整体执行，不认识具体表名：以后新增一张表只要往
`migrations/postgres/` 加一份 SQL，Go 代码一行都不用改。

- **goose 是 Go 库，不是独立容器。** 用 `github.com/pressly/goose/v3` 的 Provider API，
  直接复用 `DB.DB()` 那条连接池。本 fork 单镜像部署，外部迁移容器会让「表是否存在」
  取决于 compose 启动顺序，比 AutoMigrate 更脆弱。
- **只写 PostgreSQL 一份 SQL。** 本 fork 只跑 PG（见「数据库」），所以不写 SQLite / MySQL
  分支——多一份方言 SQL 就多一份要同步维护、却没人验证的东西。`department` 已从
  `migrateDB()` 的 AutoMigrate 列表移除，非 PG 的库会缺这张表；本 fork 不承诺 PG 之外能启动。
  测试库是例外：各测试自己 AutoMigrate 建表，不走 goose。
- **迁移文件名用 `YYYYMMDDHHMMSS_<动词>_<对象>.sql`**，与 `token-master2-dept/migrations/`
  一致，不用 goose 默认的 `00001_` 序号。序号的坑是两人同时加迁移会撞号，而时间戳天然有序。
  已执行的迁移**不能改名、不能改内容** —— goose 用文件名里的版本号记「已应用」。
- **SQL 一律 `IF NOT EXISTS`。** 已在跑的 PG 库里 `department` 表是旧版 AutoMigrate 建的，
  迁移必须在那类库上变成空操作，否则升级即失败。也正因如此，首次切换到 goose 不需要
  baseline 步骤：goose 只是把版本号写进 `goose_db_version`，表本身没变。
- **版本表名是 goose 默认的 `goose_db_version`**，与上游的 GORM 表都不撞名。
- 新增 PG 迁移：往 `migrations/postgres/` 加文件 → 用 `TEST_POSTGRES_DSN` 跑
  `TestForkMigrationsOnPostgres`。没有 PG 实例时这条用例会 SKIP，**不算验证过**。

`fork/README.md` 之外**没有**额外的迁移文档；`token-master2-dept/` 的 goose 迁移与本仓库无关，
那是业务库 `token_master` 的。

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
（第四组「手机号」见下方「四、手机号」小节；它没有单独的补丁提交哈希，
  与前三组一起随源码发布。）
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
- **没有外键就得自己保证引用完整性**：删部门与改归属都先 `SELECT ... FOR UPDATE` 锁住部门行
  （`model.LockDepartmentWithTx`），否则「数出 0 个成员 → 删除」与并发的「改归属到本部门」
  会交错，留下指向已删部门的孤儿归属。SQLite 不支持该语法，由它的单写者模型兜底。
- `users.department_id` 声明成 `*int` 而不是 `int`：GORM 零值跳过会让可空 FK 写不进 NULL。
- **不复用 `PUT /api/user/` 改部门**：那是上游自带前端在用的整行替换接口，请求体不带部门时会被
  整行覆盖清空。部门改归属走独立的 `PUT /api/user/:id/department`，且请求体**必须**显式带
  `department_id`（要清空就传 `null`）；字段缺失报错而不是当成清空，避免参数名拼错静默清空部门。
- 部门用量聚合的三个数据源各自聚合成 CTE 再 LEFT JOIN：摊平成多表 JOIN 会做
  `users × tokens × quota_data` 笛卡尔积，有 N 把秘钥的用户用量被放大 N 倍。
- **`department` 表在 PG 上由 goose 迁移建，不在 AutoMigrate 列表里**（见「数据库迁移（goose）」）。
  AutoMigrate 只会加列，改约束与回填数据都做不了，部门表下一步就要加约束与回填，
  所以一次性交给迁移工具。本 fork 不承诺 PG 之外的库，因此没有为其它方言保留 AutoMigrate 兜底。

**发布顺序**：必须先部署含这些接口的新版 BFF，再执行删掉 `token_master.department` 与
`user_profile.department_id` 的 goose 迁移（那是业务库的迁移，在 `token-master2-dept/migrations/`，
与本仓库内嵌的 goose 是两套）。旧版 BFF 仍在读写那两列，列一删就立刻报错。

`new_api` 库这一侧不需要额外步骤：`department` 表与 `users.department_id` 都随新版镜像启动
自动就位（前者由内嵌 goose 迁移建，后者仍由 AutoMigrate 加列）。

### 四、手机号

**背景**：手机号原本存在业务库 `token_master.user_profile`，与账号分属两个 database，建号因此
被拆成两段——先建账号、再补写手机号，第二段失败就是「账号已存在但手机号没有」的半成功状态。

**做法**：`users` 加一列 `phone`（`*string`），建号时与账号同一个 GORM 事务写入。

```go
Phone *string `json:"phone" gorm:"column:phone;uniqueIndex:uk_users_phone,where:phone IS NOT NULL"`
```

- **只有这一个 tag，没有格式 CHECK —— 与 `department_id` / `last_called_at` 一致。** 三者都是
  「字段 + AutoMigrate」，业务规则留给应用层。格式校验在 fork / BFF / Web 三层做
  （`^[0-9]{11}$`）。不加 DB 格式约束的理由：`new_api` 的写入方只有本仓库的 Go 代码
  （BFF 的网关库连接池带 `default_transaction_read_only=on`），没有绕过应用层的写入方；
  而加 CHECK 要多一个启动时 DDL、SQLite 测不到、CI 没有测试 job 设 `TEST_POSTGRES_DSN`
  （只有 `quay-publish.yml`），那条验证会永远 SKIP。全仓 `grep 'check:' model/` 零命中。
- **`uniqueIndex` 是必需的**：应用层预检有并发窗口，唯一约束只能由索引兜住；索引由 AutoMigrate
  建列时一并建出，零额外代码。
- **`where:phone IS NOT NULL` 不是「放过多个 NULL」的开关**：PG / SQLite / MySQL 的唯一索引本就把
  NULL 视为互不相等，去掉 WHERE 也能建出多个无号码用户（`TestMultipleUsersMayHaveNullPhone`
  两种写法都通过）。保留它只为让索引只覆盖有号码的行、与旧 `user_profile_phone_key` 同口径。
  另外只有 postgres 与 sqlite dialector 会把这个子句拼进 DDL，mysql dialector 静默丢弃、退化成
  普通唯一索引——行为等价（NULL 仍互不冲突），但别以为 MySQL 上也建出了部分索引。
- **不过滤 `deleted_at`**：软删用户仍占着号码，与原 `user_profile_phone_key` 一致。
- **`UpdateWithTx` 的 `Omit` 列表必须含 `"phone"`**：该路径用 `Updates(struct)` 整行覆盖，
  GORM 会写入**非 nil** 指针，不排除就会把调用方快照里的旧号码写回。
- **编辑路径不写 phone**：产品决定建号后手机号不可改，`user_management_fork.go` 的
  `profileUpdates` 与 `EditWithTx` 的 updates map 都不含它，请求体里带了也忽略。
- **错误码**：`PHONE_INVALID` / `PHONE_TAKEN`，与 `CreateUser` 其它业务失败（含「部门不存在」）
  同一口径：HTTP 200 + `success:false`。web 控制台按 `message_key` 翻译（`message` 保持英文），
  BFF 按 `code` 映射。
- **持久化从哪里来**：`users.phone` 由 AutoMigrate 建列与索引，**不需要 goose 迁移**。
  针对 AutoMigrate 所建表的 `ALTER TABLE` 不能放进 goose——goose 在 AutoMigrate 之前跑，
  空库上表还不存在；给它加 `to_regclass` 守卫又会被 goose 记为已应用、约束永久缺失。

## 数据库

本 fork **只跑 PostgreSQL**，自动化测试用 SQLite 内存库，MySQL 不在运行范围内。

部门表的 goose 迁移**只写 PostgreSQL 一份**（见「数据库迁移（goose）」）。`department` 已从
AutoMigrate 列表移除，因此用 SQLite / MySQL 跑生产启动会缺这张表——这是有意的，本 fork 不为
PG 之外的库保留兜底。测试库不受影响：各测试自己 AutoMigrate 建表，从不调用 `migrateDB()`。

需要留意的是：上游 new-api 本身支持 SQLite / MySQL ≥ 5.7.8 / PostgreSQL ≥ 9.6，仓库根的
`AGENTS.md` 也要求改动对三库兼容。本 fork 的手写 SQL 与错误判定目前**只按 PG + SQLite 两支写**：

- `controller/department_fork.go` 的 `isDuplicateKey` 只认 Postgres 的 `23505` 与 SQLite 的
  `UNIQUE constraint failed` 文本，没有 MySQL 的 `1062`；
- 同一文件的 `LIKE ... ESCAPE '\'` 转义写法也只保证 PG 与 SQLite。

因此若要把本 fork 部署到 MySQL，这几处必须先补 MySQL 分支并做三库实测。仅在当前 PG 部署下
运行不受影响。

## 改了哪些文件

共 29 个文件（12 个上游文件被改、17 个新增），+2431/−62（不含 `fork/` 目录本身）。

**上游文件（merge 时唯一可能冲突的地方）**

| 文件 | 改动 | 风险 |
|---|---|---|
| `model/user.go` | 不变量辅助函数、`UpdateWithTx`/`delete` 的守卫、`LastCalledAt` / `DepartmentId` / `Phone` 字段、三条扣费写入路径、`SearchUsers` 的筛选条件 | **最高**，四组定制都改它 |
| `controller/user.go` | `CreateUser` 允许 root 建 root、接受部门与手机号；`ManageUser` 删两处硬拒、新增两个 action、扩写 demote 分支；`SearchUsers` 加筛选 | **高** |
| `router/api-router.go` | 三处插路由：`/api/department/`、`PUT /api/user/:id/department`、`GET /api/data/departments` | 中，前三组都插在同一文件 |
| `controller/audit.go` | 审计 action 英文模板 map 加 4 项 | 低 |
| `service/security_verification.go` | proof action 白名单加 2 项 | 低 |
| `model/main.go` | `migrateDB()` 开头调 goose 迁移；AutoMigrate 列表**移除** `&Department{}` | 低 |
| `model/task_cas_test.go` | 测试的 `AutoMigrate` 同样加 `&Department{}` | 低 |
| `model/user_pagination_test.go` | `SearchUsers` 签名变化，补两个参数 | 低 |
| `controller/security_enrollment_test.go` | 绑定清单加用例 | 低 |
| `model/user_authentication_test.go` | 补 `deleteUserAuthenticationData` 的用例（只清目标用户、软删行、失败传播） | 低 |

**本 fork 新增（文件名带 `_fork` 后缀，上游不可能改，merge 时不会冲突）**

| 文件 | 内容 |
|---|---|
| `model/department_fork.go` | `Department` 模型、名字归一与校验、`LockDepartmentWithTx` / `EnsureDepartmentExistsWithTx` |
| `model/user_phone_fork.go` | `IsPhoneDuplicate`：只认 `uk_users_phone` 唯一索引冲突（PG 看约束名、SQLite 看错误文本） |
| `controller/user_phone_fork.go` | `normalizePhoneFork` 归一与 11 位校验、`ensurePhoneFreeWithTx` 事务内预检、两条 `common.Message` 错误 |
| `model/goose_fork.go` | 内嵌 PG 迁移的 goose 集成：`Provider.Up()`、非 PG 库直接拒绝 |
| `migrations/embed.go` | 内嵌 `migrations/postgres/*.sql`，导出 `migrations.Postgres` |
| `migrations/postgres/*.sql` | 部门表建表迁移（仅 PG） |
| `controller/department_fork.go` | 四个 CRUD handler + `PUT /api/user/:id/department` + `isDuplicateKey` |
| `controller/department_usage_fork.go` | 部门用量聚合 handler |
| `controller/department_fork_test.go`、`controller/department_usage_fork_test.go`、`controller/user_department_fork_test.go`、`controller/user_multi_root_fork_test.go`、`controller/user_search_department_fork_test.go` | controller 包测试 |
| `model/department_fork_test.go`、`model/user_department_fork_test.go`、`model/user_last_called_fork_test.go`、`model/user_multi_root_fork_test.go`、`model/user_search_filter_fork_test.go` | model 包测试 |

（另有 3 个上游测试文件被改签名或补迁移。）上游若在这些路径放了同名文件，
git 会报 `add/add` 冲突——这是唯一需要留意的情形。

`model/user.go` 里有一段 `User` 结构体的整段对齐格式化 diff（加字段导致列宽变化），属噪声，
不影响行为，但会让该文件的 diff 看起来比实际改动大。

## 标记约定

每处改动或紧邻的一组改动上方（只标改动块，不给整个函数加标记）：

```go
// [FORK] <一句话说明为什么这块和上游不同>
```

当前共 50 处、12 个文件：

| 文件 | 处数 |
|---|---|
| `model/user.go` | 13 |
| `controller/user.go` | 10 |
| `model/user_last_called_fork_test.go` | 6 |
| `controller/department_fork.go` | 6 |
| `model/department_fork.go` | 6 |
| `router/api-router.go` | 3 |
| `model/main.go`、`model/goose_fork.go`、`controller/department_usage_fork.go`、`controller/audit.go`、`controller/security_enrollment_test.go`、`service/security_verification.go` | 各 1 |

```bash
grep -rn "\[FORK\]" --include="*.go" .
```

`model/main.go` 里 `migrateDB()` 那一行、`model/user_pagination_test.go`、`model/task_cas_test.go`
的改动没有全部加标记——它们是机械连带（先跑 goose、签名要跟着改），单看那一行就能看懂，
加标记反而增加 merge 冲突面。判断是否漏改时，用上表的文件清单核对，不要只看 grep 结果。

**新增文件放哪**：`UpdateUserDepartment` 曾经写在 `controller/user.go`（上游文件）里，后来搬到
`controller/department_fork.go`。它是纯新增 handler，同包就能拿到 `canManageTargetRole` 与
`recordManageAuditFor`，留在 `_fork` 文件里能让 `user.go` 这个冲突最高的文件少一段。

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
mkdir -p web/dist && touch web/dist/index.html   # 根包 //go:embed web/dist，见「验证」
make test
```

merge 无冲突时直接进验证。有冲突时按下面的原则处理。

### 冲突处理原则

按概率降序：`model/user.go` → `controller/user.go` 的 `ManageUser` switch → `router/api-router.go`
→ `SearchUsers`/`GetAllUsers`（部门筛选改了查询构造）→ 白名单与审计 map。

- **先接受上游版本，再按 `// [FORK]` 逐块重落。** 不要保留整段旧代码试图「合并语义」。
- 守卫**永远**在事务内、永远在写之前，不因上游重构挪到控制器。
- `adminUserManageActions` 是白名单，上游新增 action 时**取并集**。
- 部门相关的手写 SQL（删除计数、用量聚合）都写 `department`（单数），
  `Department.TableName()` 必须保持单数，否则上游若改动默认命名策略会静默错表。
- 删部门与改归属的部门行锁（`model.LockDepartmentWithTx`）不能省。部门有意不加外键，
  这把锁是唯一挡住「孤儿归属」的东西；上游若改 `lockForUpdate`，两处调用点要一起验证
  （`TestLockDepartmentWithTxEmitsRowLock` 会直接变红）。
- `SearchUsers` 的参数列表是定制扩展过的。上游若重构它（例如换成 options 结构体），
  跟着上游的新签名走，把 `departmentID`/`username`/`displayName` 作为新字段塞进去，不要保留旧签名。
- `model/user.go` 里 `User` 结构体的对齐格式差异：只保留字段新增，不要为消掉格式 diff 去重排上游字段。
- **上游若开始用 goose 或其它迁移工具**：不要直接采纳。本 fork 的 goose 接线
  （`model/goose_fork.go` 与 `migrations/`）与 `goose_db_version` 表必须与上游方案共存或合并，
  否则同一次启动会建两套版本表。先确认上游的版本表名与方言，再决定是并进去还是改本 fork 的路径。
- **`model/main.go` 的 AutoMigrate 列表**：上游新增模型时正常取并集，但**不要**把
  `&Department{}` 加回去——部门表归 goose 管，两套机制同时管一张表会导致每次重启都尝试改列。

### 验证

用仓库自带的入口，与 CI（`.github/workflows/ci.yml`）同一套命令：

```bash
mkdir -p web/dist && touch web/dist/index.html   # 根包 //go:embed web/dist，见下
go vet ./... && go build ./...                   # 根模块
(cd relaykit && go vet ./... && go build ./...)  # 子模块各自独立可编译
(cd tokenkit && go vet ./... && go build ./...)
make test                                        # 根模块 + relaykit + tokenkit 全部包
gofmt -l controller model service middleware     # CI 不查格式，本地补一刀
```

`make test` 覆盖所有包，包括 `middleware/`、`router/` 与两个子模块；这比只测
`model`/`controller`/`service` 三个包更严。

两点必须知道：

- **`web/dist` 先占位。** `main.go` 用 `//go:embed web/dist` 硬编前端产物，而该目录在
  `.gitignore` 里、只在 Dockerfile 第一阶段构建前端后才存在。不占位时 `go build ./...` 必然失败，
  这不是代码问题。`make test` 会自己跳过根包，所以只跑测试可以不占位。
- **本机需要 Go 1.26.10**（见 `go.mod`）。没装的话要么 `brew install go`，要么把仓库挂进
  `golang:1.26.10-alpine` 容器里跑上面这几条——注意 Docker 内存低于 4G 时 `go build ./...`
  容易被 OOM kill，`signal: killed` 就是这个原因，不是编译错误。

仅编译通过不算升级成功——下面「测试覆盖不到」那五项仍需人工验证。

### 并发用例（可选，需要 Postgres）

`TestConcurrentRevokeLeavesOneEnabledRoot` 在默认校验里 FAIL/SKIP（未设 `TEST_POSTGRES_DSN`），
因此最核心的不变量「至少留一个启用的 root」缺少自动化覆盖。要真跑它：

```bash
docker run --rm \
  -v /绝对路径/new-api:/src -v newapi-gomod:/go/pkg/mod -w /src \
  -e GOWORK=off -e GOPROXY=https://goproxy.cn,direct \
  -e TEST_POSTGRES_DSN='postgres://user:pass@host:5432/newapi_test?sslmode=disable' \
  golang:1.26.10-alpine \
  go test ./model/ -run TestConcurrentRevokeLeavesOneEnabledRoot -count=1 -v
```

> **DSN 必须指向独占的测试库，且库名必须含 `test`。** 该用例会 `DROP TABLE users` 再重建；
> 库名不含 `test` 时直接 FAIL 而不是照跑，防止误连共享实例清空账号。用例内部还会临时把主库
> 类型切换为 PostgreSQL，否则 `lockForUpdate` 在 SQLite 下不会发 `FOR UPDATE`。

### 构建镜像

仓库根的 `Dockerfile` 就是生产镜像的定义（第一阶段 bun 构建 `web/dist`，第二阶段编 Go），
直接 `docker build` 即可：

```bash
cd <源码目录>
docker build -t new-api:$(date +%Y%m%d)-$(git rev-parse --short HEAD) .
```

tag 用 `<构建日期>-<HEAD 短号>`，一眼看出镜像来自哪天哪个提交。构建时间包含一次
`bun install` 加前端构建，不能跳过。

> 源码里四组定制（多 root、最近调用、部门、手机号）恒在，tag 不编码定制组，也不跟随上游版本号。
> 工作区有未提交改动时 tag 只反映 HEAD，不反映改动，别用它反推镜像内容。

国内网络通常拉不到 registry 与 Go proxy，用 `--build-arg` 显式传代理，**不要**指望 shell 里的
`HTTPS_PROXY` 自动生效：

```bash
docker build \
  --build-arg HTTP_PROXY=http://<容器可达的宿主机 IP>:<端口> \
  --build-arg HTTPS_PROXY=http://<容器可达的宿主机 IP>:<端口> \
  -t new-api:$(date +%Y%m%d)-$(git rev-parse --short HEAD) .
```

宿主机上的 `127.0.0.1:xxxx` 进了容器指向容器自身，传进去等于没配。要填**容器可达**的地址
（colima 下一般是宿主内网 IP）。

> **tag 是构建时的快照，不代表源码基线是上游某个版本。** 上线前必须跑完整回归。
> 本 fork 的基线提交是 `6ef123cd5`。

## 新增接口能力

- `POST /api/user/manage` 的 `action` 多了 `grant_root`、`revoke_root`。请求体形如
  `{"id": <目标用户 id>, "action": "grant_root"}`，需带 `X-Security-Proof`
  （scope `admin.user.manage`，context `{"user_id", "action"}`）。
- `GET /api/user/`、`GET /api/user/search`、`GET /api/user/:id` 的响应里 `User` 多了
  `last_called_at`（秒级 Unix 时间，`0` 表示从未调用）与 `department_id`（`null` 表示未分配）。
- `GET /api/user/search` 多了三个精确筛选参数：`department_id`、`username`、`display_name`。
  非法 `department_id` 显式报错而不是静默忽略。注意响应是 **HTTP 200 + `success: false`**
  （`common.ApiErrorT` → `common.ApiError` 固定 200，与上游所有控制台错误一致），
  调用方要按 `success` 判成败，不能按 4xx 判。
- `POST /api/user/` 接受 `department_id`，并在响应 `data.id` 里回传新用户 id。
- `GET|POST|PUT /api/department/`、`DELETE /api/department/:id`：部门字典读写。
- `PUT /api/user/:id/department`：改单个用户的部门归属，请求体 `{"department_id": <id>}`，
  传 `null` 表示清空；**不带该字段返回 `Invalid parameters`**，不会被当成清空。
- `GET /api/data/departments?start_timestamp=&end_timestamp=`：部门用量聚合，
  返回 `members`/`keys`/`calls`/`tokens`/`quota`。

调用约定：`grant_root` 只有 root 能调，`revoke_root` 受「至少留一个启用 root」约束；两者都要求
目标用户存在且状态合法。部门的写操作与改归属都受 `canManageTargetRole` 约束（同级或更高角色的
账号不可操作）。

## 测试覆盖不到的五点

自动化测试覆盖了不变量、写路径与接口行为，但以下五项只能人工验证：

1. **`revoke_root` 的 Casbin override 清理**：现有测试无法区分「走了 `demote` 分支」与「走了
   `else` 分支」，两条都会把 role 写成 1。唯一手段是在 Postgres 上直接查 `casbin_rule`
   （目标用户先 `SetUserPermissions`，撤销后应无 override）。
2. **`last_called_at` 的端到端可见性**：需要真实扣费后查库，确认时间戳是秒级真实时刻，
   且管理员充值、退款不会刷新它。
3. **部门用量聚合的数字正确性**：测试用的是造出来的 `quota_data`/`tokens` 行，能验证 CTE 结构与
   零成员部门不丢失，但真实库里「一个用户多把秘钥」「跨部门迁移后历史流水仍记在旧账上」等
   形态要拿生产数据抽样核对。特别地，`keys` 只统计成员**当前**拥有的有效秘钥数。
4. **前端渲染**：本仓库前端未改动，若在自己前端展示这些字段/接口，属另一侧的验证。
5. **goose 迁移只有 PG 一份，且需要真实 PG 才能验证。** `TestForkMigrationsOnPostgres`
   验证建表、唯一索引、重复执行幂等，但它要求 `TEST_POSTGRES_DSN`，没配时 SKIP——
   **SKIP 不等于通过**。上线前必须对真实 PG 跑一次，覆盖「空库启动」与「已有 department 表
   （旧版 AutoMigrate 建的）启动」两种形态，并确认 `goose_db_version` 里记下了版本、
   `\d department` 与旧表结构一致。

## 已知限制（有意不修）

| 现象 | 说明 |
|---|---|
| root 之间可互相重置密码、互相禁用、互相撤销 root | step-up 只证明「操作者是某个 root」，不区分是哪一个。这是多 root 的固有代价，不是缺陷。全部动作进上游审计。 |
| `grant_root` 会吊销目标会话 | role 变更走 `UpdateWithTx` 的 `authChanged` 分支，递增 auth version → `Update` 调 `RevokeAllUserSessions`。上游 `promote` 走同一路径，所以升权也会踢下线；目标用户需要重新登录。降权同理，且即时生效（鉴权读实时角色）。 |
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
