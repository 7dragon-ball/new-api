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
- **AutoMigrate 管着的表不写迁移。** `logs`、`quota_data`、`redemptions` 都在 `migrateDB()` 的
  AutoMigrate 列表里（独立日志库还有 `migrateLOGDB()` 再补一次），加列本来就在建表/启动时完成；
  再写一份 goose SQL 就是同一列定义两处维护，改 tag 忘了改 SQL 只换来一次多余 `ALTER` 或迁移失败，
  而独立日志库那份 SQL 压根不会被执行（goose 只跑主库）。所以身份快照这几列**只有 GORM tag**，
  升级路径由 `TestForkMigrationsOnPostgres` 的「旧形状表 + AutoMigrate」用例验证。

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
（第四组「手机号」见下方「四、手机号」小节，第五组「上游密钥指纹」见「五、日志记录上游密钥指纹」；
  两者都没有单独的补丁提交哈希，与前三组一起随源码发布。）
（最新）    feat(user): 新增批量重置用户密码接口（仅后端）
```

基线是上游 `6ef123cd5`。这些都是**可直接 merge 的源码改动**，不是补丁。最后一条不记短 hash——
它会跟着 `git commit --amend` 变，按提交标题 grep 即可。

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

### 五、日志记录上游密钥指纹

**上游行为**：日志只把 `other.admin_info.multi_key_index`（密钥在渠道里的位置）记为「这次用了哪把钥匙」。位置不稳定：渠道增删或重排密钥后，同一序号指向另一把，历史日志的掩码、`/reveal` 明文与「按密钥明文反查」的结果全部漂移。

**做法**：写日志时把该次调用所用密钥做 sha256、取前 16 位，写入 `other.admin_info.channel_key_fp`。取的是**日志写入时刻的 `ContextKeyChannelKey`**，不是 `GetNextEnabledKey()` 的返回值——插件类渠道会在选定后把该键改写成刷新后的凭据行，且该行会回写 `channels.key`，所以上下文值才是管理员能从库里读到的那份明文。该键在 `middleware/distributor.go` 里无条件设置（不在 `IsMultiKey` 分支内），因此单密钥渠道与错误日志同样带得上指纹。

**哈希输入不做任何规范化**，连 trim 都不做。两侧输入本来就同源（都是 `channels.key` 里那一行的字节），而 Go 的 `strings.TrimSpace` 与 JS 的 `trim()` 覆盖的字符集不同——U+FEFF 对 JS 是空白、对 Go 不是，U+0085 反过来。加 strip 只会制造两侧永远对不上的指纹，表现是「按密钥搜不到」。

**已知缺口**：升级前的日志没有该字段；JSON 数组形式的渠道密钥（Vertex 等服务账号）不参与反查；任务退款/差额结算日志（`RecordTaskBillingLog`）只有 `context.Context`，不带指纹。详见 `fork/design-channel-key-fingerprint.md`。

**发布顺序**：先发含此改动的新 API 镜像，再发按指纹搜索的 BFF。反过来的表现是「按密钥搜永远空页」，不报错。

### 六、批量重置用户密码

**需求**：一次 step-up 授权覆盖一批账号（现场交付默认密码、集中回收账号），任一目标不可重置则
整批回滚、零写入、不返回任何密码。**禁用状态的账号也属于可重置目标**：现场流程是先禁用该回收的
账号、再统一改密，若把 `status` 当拒绝条件，管理员就得先启用一遍再禁用回去。

**做法**：`POST /api/user/batch/reset_password`，body 是 `ids` 数组（≤100），响应逐条返回新密码。
新增 scope `admin.user.password.batch_reset`，context 是「升序去重后的完整 id 集合」，所以一份
proof 只能用在签发时确认的那一批账号上。逐目标沿用 `canManageTargetRole`；进事务后按 id 升序
加锁（固定顺序防死锁），锁内重读 `role`/`deleted_at` 再授权一次（`status` 有意不重读，禁用账号
允许被重置，且重置不会改动 `status`）——事务外那次预检只用于尽早拒绝，不构成授权保证。密码 8 位、必含大写/小写/数字、`crypto/rand` 生成、
`HashAccountPassword` 散列；散列在事务外算完，事务里只写 `auth_version`（先）与 `password`（后）。

**已知代价**（有意接受，不修）：

| 现象 | 说明 |
|---|---|
| 8 位密码熵约 47 bit | 明确的产品要求。后端生成、不进日志、重置后吊销浏览器会话，降低撞库价值。 |
| 禁用账号也能被重置 | 有意为之（见「需求」）。重置只换凭据、不碰 `status`，所以不会顺手把账号启用回来。 |
| 重置密码踢不掉现有 PAT | `user_access_tokens` 没有 auth version 列，PAT 鉴权不与版本号比较。要连带撤销只能物理删令牌。与上游三条改密路径一致。 |
| 回滚不是完全无痕 | `IncrementUserAuthVersionWithTx` 在写库前就发了 Redis pending fence，事务回滚后 fence 只能等 TTL 过期（见「测试覆盖不到」第 6 条）。 |
| 请求不幂等 | 响应丢了，密码就只剩已改掉的散列；proof 单次使用，重放会被拒。调用方不得盲目重试。 |

**本仓库前端未接入**：接口只对 BFF / 脚本开放（与 `last_called_at`、部门接口相同）。审计 action
`user.batch_password_reset` 也没登记进 `web/src/features/usage-logs/lib/format.ts` 的
`AUDIT_TEMPLATES`，控制台审计列表显示原始英文（与 `department.*` 一致）。

### 七、身份快照（`feat/identity-snapshot` 的四个提交）

**需求**：用户、部门、渠道都是**硬删除**（或改名）。删掉之后，历史日志/用量/兑换记录再没有任何地方
能回答「当时是谁、哪个部门、哪个渠道」。所以要在产生数据的那一刻把名字抄一份存进去。

**做法**：写入时抄名字，查询直接显示，不回查当前状态。

- **热路径零查库**：鉴权中间件把 `display_name` / `department_name` 从用户缓存写进 gin 上下文
  （`UserBase.WriteContext` → `constant.ContextKeyUserDisplayName` / `ContextKeyUserDepartmentName`）；
  分发中间件写渠道名。写日志/用量时只读上下文，一次额外查询都不发。
- **缓存必须带名字**：`UserBase` 新增 `DisplayName` / `DepartmentName` 两个**值**字段，
  `userCacheSchemaVersion` 升到 3，`user_auth_cache.go` 的内联 Lua 把两列连同 MAC/版本号整份写入。
  部门名只能按 `user.DepartmentId` 查字典表，所以「写缓存」与「冷启动返回」共用
  `baseUserWithDepartment`——三条旁路（populate / update / 冷启动）任何一条漏了，都会把部门名冲成空串。
  部门 **id 不进** `UserBase`：`PublishUserAuthCache` 用 `*a == *b` 判断元数据是否还在变，
  指针字段会让它永远判成在变，重试三次后报错。
- **没有请求上下文的路径**（任务计费、充值回调、系统日志）按 `userId` 从用户缓存取
  （`identitySnapshotForUser`），不额外查库。
- **放哪里**：`logs.display_name` / `logs.department` / `logs.channel_name`（同机制还有
  `logs.channel_tag` / `logs.token_owner`，取 `channels.tag` 与 `tokens.owner`），
  `quota_data.display_name` / `quota_data.department`，
  `redemptions.used_username` / `redemptions.used_display_name`。全列 TEXT：源列宽度不统一
  （`users.display_name` 的 20 字上限只在请求校验层，OAuth 注册绕过它；`channels.name` 根本没限），
  快照列收窄会让超长名字的 INSERT 失败，而 `createLog` 只记 error → 静默丢日志。
- **身份进聚合键**：`quota_data` 的聚合键加了 `display_name`/`department`，所以改名前后是两行，
  历史不会被改后的名字覆盖。键的唯一定义是 `model/usedata.go` 的 `quotaDataKeyParts()`：
  内存分桶键与两条 upsert 判据（`Find` 的 Where、`UPDATE` 的 Where）都由它生成，不再手抄三份。
- **不回填存量**：老日志/老用量/老兑换记录的身份列留空（NULL/空串），语义是「当时没记」。
- **跨仓库契约（BFF 侧）**：上述列在本分支上线后才存在，且不保证有值；读之前先判空。
  `quota_data` 的存量行是 NULL 而不是空串，拼接/展示时按「未知」处理。老日志按旧列名（`users`
  反查）展示的旧逻辑不改也不补——两套数据共存时，新行显示快照，老行显示当前值/空。

**已知缺口**：

- 只对新建行生效，存量不回填、不做修复脚本（与渠道秘钥指纹同一形态）。
- **部门改名不会立刻刷新成员缓存**：`UpdateDepartment` 只改 `department` 表，`user:<id>` 里的
  `DepartmentName` 要等缓存过期（`SYNC_FREQUENCY`，默认 60s）才重建，所以这段时间内新产生的
  日志/用量仍写旧名，`quota_data` 会把旧名/新名拆成两行。当前业务没有部门改名操作，故未加
  「按部门批量刷成员缓存」的代码。若以后要启用改名，在改名成功后按 `department_id` 取成员 id
  逐个 `PublishUserAuthCache`——不要用 `invalidateUserCache`：那会连缓存里的 `Quota` 一起丢掉，
  而批量更新模式下数据库余额还没跟上缓存，重建时会读出偏高的余额。

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

### 独立日志库（ClickHouse）不在本部署范围内

本 fork 只把日志表放在主库（`LOG_SQL_DSN` 不设置，`InitLogDB` 直接 `LOG_DB = DB`）。上游支持的
「独立日志库」——包括 ClickHouse（`LOG_SQL_DSN=clickhouse://…`）——本部署不使用，也没有实测：

- 身份快照新增的列（`display_name` / `department` / `channel_name` / `channel_tag` / `token_owner`）
  已同步进 `model/main.go` 的 ClickHouse 建表语句（`clickHouseLogCreateTableSQL`），保证代码自洽；
- 但**老 ClickHouse 日志库升级不补列**：`CREATE TABLE IF NOT EXISTS` 在已存在的表上是空操作，
  且本 fork 未实现 `ALTER TABLE logs ADD COLUMN`。若将来真的要启用 ClickHouse，必须先补这一步，
  否则老库升级后每条日志写入都会以 `NO_SUCH_COLUMN_IN_TABLE` 失败。
- 不启用 ClickHouse 时，日志表随主库走 PG，由 `migrateDB()` 的 `AutoMigrate`（含 `&Log{}`）
  在建表/启动时建列、补列，不受上述限制影响。

## 改了哪些文件

共 52 个文件（32 个上游文件被改、20 个新增），+3587/−137（不含 `fork/` 目录本身；
含 `feat/identity-snapshot` 的 4 个提交与随后的修正）。

**上游文件（merge 时唯一可能冲突的地方）**

| 文件 | 改动 | 风险 |
|---|---|---|
| `model/user.go` | 不变量辅助函数、`UpdateWithTx`/`delete` 的守卫、`LastCalledAt` / `DepartmentId` / `Phone` 字段、三条扣费写入路径、`SearchUsers` 的筛选条件 | **最高**，四组定制都改它 |
| `controller/user.go` | `CreateUser` 允许 root 建 root、接受部门与手机号；`ManageUser` 删两处硬拒、新增两个 action、扩写 demote 分支；`SearchUsers` 加筛选 | **高** |
| `router/api-router.go` | 四处插路由：`/api/department/`、`PUT /api/user/:id/department`、`GET /api/data/departments`、`POST /api/user/batch/reset_password` | 中，定制都插在同一文件 |
| `controller/audit.go` | 审计 action 英文模板 map 加 5 项 | 低 |
| `service/security_verification.go` | 新增 1 个 proof scope 常量，白名单与「要求表」各加 1 项 | 低 |
| `service/access_token_scope.go` | proof scope → PAT scope 表新增 `admin.user.password.batch_reset` → `user:write` | 低 |
| `middleware/access_token_routes.go` | PAT 路由规则新增 `POST /api/user/batch/reset_password` → `user:write` | 低 |
| `model/main.go` | `migrateDB()` 开头调 goose 迁移；AutoMigrate 列表**移除** `&Department{}` | 低 |
| `model/task_cas_test.go` | 测试的 `AutoMigrate` 同样加 `&Department{}` | 低 |
| `model/user_pagination_test.go` | `SearchUsers` 签名变化，补两个参数 | 低 |
| `controller/security_enrollment_test.go` | 绑定清单加用例 | 低 |
| `model/user_authentication_test.go` | 补 `deleteUserAuthenticationData` 的用例（只清目标用户、软删行、失败传播） | 低 |
| `service/log_info_generate.go` | `AppendRelayLogAdminInfo` 加一行调用写指纹 | 低，块内独立 |
| `service/violation_fee.go` | 构建违规费日志处加一行调用写指纹 | 低 |
| `model/user.go` | `UserBase` 加 `DisplayName` / `DepartmentName` 两个值字段（快照来源） | 低 |
| `model/log.go` | `Log` 加快照列；写入路径按上下文/用户缓存填快照；列表与导出反查不到渠道时保留行内快照 | 中，写入路径多，漏一处就少一列 |
| `model/usedata.go` | `QuotaData` 加 `display_name` / `department` 并入聚合键；`quotaDataKeyParts()` 成为 10 列键的唯一定义 | 中，聚合键写错只错数据、不报错 |
| `model/user_cache.go` | `baseUserWithDepartment` 成为「用户 → 带部门名的 `UserBase`」的唯一实现，三个缓存路径共用 | 中 |
| `model/user_auth_cache.go` | 内联 Lua 多写两列（ARGV 下标必须与 `UserBase` 字段顺序对齐）；`userCacheSchemaVersion` 升 3 | **最高**，漏写一列就永远读回空串 |
| `model/redemption.go` | 兑换时抄一份兑换人用户名/显示名 | 低 |
| `constant/context_key.go` | 新增 `ContextKeyUserDisplayName` / `ContextKeyUserDepartmentName` | 低 |
| `middleware/auth.go` | 鉴权通过后把身份写进上下文 | 低 |
| `middleware/distributor.go` | 分发后把渠道名/供应商写进上下文 | 低 |
| `service/task_billing.go` | `LogTaskConsumption` 加一行调用写指纹（任务型渠道的提交日志） | 低 |
| `relay/responses_websocket.go` | lockedContext 白名单提成包级变量 `lockedContextKeys`（行为不变，为让白名单可被测试断言） | 低 |

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
| `service/channel_key_fingerprint_fork.go` | 上游密钥指纹计算与写入 admin_info |
| `service/channel_key_fingerprint_fork_test.go` | 指纹向量表（与 BFF 侧共用）、写入路径测试 |
| `model/log_identity_snapshot_fork_test.go` | 身份快照的写入/读取侧回归（上下文、后台路径、删除后列表） |
| `model/user_cache_fork_test.go` | 缓存往返、`updateUserCache`、冷启动返回值、Redis 关闭时不查部门 |
| `model/usedata_fork_test.go` | `quota_data` 按身份快照分桶、同键累加的回归 |
| `common/random_password_fork.go` | 8 位随机账号密码：三类字符必现、Fisher–Yates 打散位置、`crypto/rand` 无偏 |
| `model/user_batch_password_fork.go` | `LockUserForBatchPasswordResetWithTx`（Unscoped 行锁，软删行也要锁得到） |
| `controller/user_batch_password_reset_fork.go` | 批量重置 handler + 事务体 `resetBatchPasswordsInTx`（锁内二次授权在那里） |
| `service/security_verification_batch_password_fork.go` | proof context 定义、id 集合校验（≤100、升序、不许重复） |
| `controller/user_batch_password_reset_fork_test.go` | controller 包测试（含锁内二次授权与整批回滚） |

（另有 3 个上游测试文件被改签名或补迁移。）上游若在这些路径放了同名文件，
git 会报 `add/add` 冲突——这是唯一需要留意的情形。

`model/user.go` 里有一段 `User` 结构体的整段对齐格式化 diff（加字段导致列宽变化），属噪声，
不影响行为，但会让该文件的 diff 看起来比实际改动大。

## 标记约定

每处改动或紧邻的一组改动上方（只标改动块，不给整个函数加标记）：

```go
// [FORK] <一句话说明为什么这块和上游不同>
```

当前共 104 处、37 个文件：

| 文件 | 处数 |
|---|---|
| `model/user.go` | 17 |
| `controller/user.go` | 16 |
| `service/security_verification.go` | 7 |
| `model/user_last_called_fork_test.go` | 6 |
| `model/department_fork.go` | 6 |
| `controller/department_fork.go` | 6 |
| `model/usedata.go` | 5 |
| `router/api-router.go` | 4 |
| `model/log.go` | 4 |
| `constant/context_key.go` | 3 |
| `service/security_verification_fork.go` | 2 |
| `model/user_auth_cache.go` | 2 |
| `controller/audit.go` | 2 |
| `common/random_password_fork.go`、`controller/department_usage_fork.go`、`controller/security_enrollment_test.go`、`controller/user_batch_password_reset_fork.go`、`controller/user_management_fork.go`、`controller/user_unified_update_fork_test.go`、`middleware/access_token_routes.go`、`middleware/auth.go`、`middleware/distributor.go`、`migrations/embed.go`、`model/goose_fork.go`、`model/main.go`、`model/redemption.go`、`model/user_batch_password_fork.go`、`model/user_cache.go`、`model/user_department_fork_test.go`、`model/user_phone_fork.go`、`relay/responses_websocket.go`、`service/access_token_scope.go`、`service/channel_key_fingerprint_fork.go`、`service/log_info_generate.go`、`service/security_verification_batch_password_fork.go`、`service/task_billing.go`、`service/violation_fee.go` | 各 1 |

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

仅编译通过不算升级成功——下面「测试覆盖不到」那六项仍需人工验证。

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
- `POST /api/user/batch/reset_password`：批量重置密码，body 是 `ids` 数组（≤100、不许重复），
  需带 `X-Security-Proof`（scope `admin.user.password.batch_reset`，context 的 `user_ids`
  必须是升序去重后的完整 id 列表）。全或无：任一目标不可重置则整批回滚、不返回任何密码；
  成功时逐条返回 `id`/`username`/`display_name`/`email`/`password`，顺序与请求一致。
  **明文密码只在这一响应里出现一次**，请求不幂等，响应丢了只能重新签 proof 重来。
- 日志行（`GET /api/log/`、`/api/log/export`）多了快照字段 `display_name`、`department`、
  `channel_name`、`channel_tag`、`token_owner`。**筛选参数没变**：`LogFilter` 没有按显示名/渠道名
  筛选的能力，这些列只用于展示。

调用约定：`grant_root` 只有 root 能调，`revoke_root` 受「至少留一个启用 root」约束；两者都要求
目标用户存在且状态合法。部门的写操作与改归属都受 `canManageTargetRole` 约束（同级或更高角色的
账号不可操作）。

## 测试覆盖不到的六点

自动化测试覆盖了不变量、写路径与接口行为，但以下六项只能人工验证：

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
6. **批量重置回滚后的 Redis fence 残留。** `IncrementUserAuthVersionWithTx` 在写库之前就发布了
   pending fence，事务回滚后它只能等 TTL（`userAuthFenceTTLSeconds`，约 120 秒）过期；这期间
   已处理过的用户读写路径返回 `ErrUserAuthCachePending`，登录与刷新暂时失败。测试库不启 Redis，
   该窗口没有自动化覆盖。要验证得在真实 Redis 上让一次批量重置中途失败，观察 fence TTL 与
   恢复时间。

## 已知限制（有意不修）

| 现象 | 说明 |
|---|---|
| root 之间可互相重置密码、互相禁用、互相撤销 root | step-up 只证明「操作者是某个 root」，不区分是哪一个。这是多 root 的固有代价，不是缺陷。全部动作进上游审计。 |
| `grant_root` 会吊销目标会话 | role 变更走 `UpdateWithTx` 的 `authChanged` 分支，递增 auth version → `Update` 调 `RevokeAllUserSessions`。上游 `promote` 走同一路径，所以升权也会踢下线；目标用户需要重新登录。降权同理，且即时生效（鉴权读实时角色）。 |
| 渠道测试固定取「第一个 root」当测试用户 | 多 root 下取到哪个不确定，行为可接受。 |
| 渠道通知默认发给所有未显式配置的 root | 多 root 后收件人变多，行为可接受。 |
| 关闭 Redis 的部署写不出部门快照 | 部门名只能从鉴权缓存带下来；`RedisEnabled=false` 时 `GetUserCache` 直接返回 `ToBaseUser()`（否则每个请求都要多查一次字典表），因此 `logs.department` / `quota_data.department` 恒为空。显示名不受影响。生产多节点部署必须开 Redis。 |
| 已存在的 ClickHouse 日志库升级后缺快照列 | 见「数据库」→「独立日志库（ClickHouse）不在本部署范围内」：建表语句已补列保证代码自洽，但不做老库自动补列。 |
| 部门用量聚合仍按当前归属算 | `GET /api/data/departments` 用 `users.department_id` 实时聚合（且 `deleted_at IS NULL`），用户转部门后历史用量跟着走，删掉的用户不计入。`quota_data.department` 快照已写入但尚未被该接口使用。 |
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
