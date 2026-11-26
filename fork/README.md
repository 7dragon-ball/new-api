# fork/ — New API 的定制登记册

本仓库是 [New API](https://github.com/QuantumNous/new-api)（AI API 网关）的 fork，在上面做了多组
后端定制：支持多个超级管理员（root）、在 `users` 表记录真实的最近调用时刻、把部门归属从业务库
搬到 `new_api` 库、把手机号也搬进 `new_api.users`、日志记录上游密钥指纹、批量重置用户密码、
按供应关系设优先级。各组细节见下方「定制清单」。

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
  第六组「批量重置密码」、第七组「身份快照」、第八组「新建渠道按凭据测试连通性」、
  第九组「供应关系优先级」见对应小节；它们都没有单独的补丁提交哈希，与前三组一起随源码发布。）
```

基线是上游 `6ef123cd5`。这些都是**可直接 merge 的源码改动**，不是补丁。后四组不记短 hash——
提交会跟着 `git commit --amend` / rebase 变，按提交标题 grep 即可。

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

> **自带控制台不显示这一列。** 本 fork 对 `web/` 的改动只有 i18n 语言包、`static-keys.ts` 的白名单
> 与 `secure-verification/types.ts` 的 scope 联合类型（见「改了哪些文件」），没有任何页面组件接线，
> 所以 `last_called_at` 仍只供自行开发的管理端读取。

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
  `quota_data` 的存量行在这两列上是 NULL、新行是空串，所以 upsert 判据对这两列用
  `COALESCE(col, '') = ?` 比较（`applyQuotaDataKey`）——直接 `= ''` 匹配不到 NULL，部署后
  首次保存就会给同一聚合键再插一行。NULL 本身不改写，仍是「当时没记」。
- **跨仓库契约（BFF 侧）**：上述列在本分支上线后才存在，且不保证有值；读之前先判空。
  `quota_data` 的存量行仍可能是 NULL，拼接/展示时按「未知」处理。老日志按旧列名（`users`
  反查）展示的旧逻辑不改也不补——两套数据共存时，新行显示快照，老行显示当前值/空。

**已知缺口**：

- 只对新建行生效，存量不回填、不做修复脚本（与渠道秘钥指纹同一形态）。
- **部门改名不会立刻刷新成员缓存**：`UpdateDepartment` 只改 `department` 表，`user:<id>` 里的
  `DepartmentName` 要等缓存过期（`SYNC_FREQUENCY`，默认 60s）才重建，所以这段时间内新产生的
  日志/用量仍写旧名，`quota_data` 会把旧名/新名拆成两行。当前业务不会改名，接口虽仍在
  `PUT /api/department/` 上可达，但没有调用方，故未加「按部门批量刷成员缓存」。若以后要启用
  改名，在改名成功后按 `department_id` 取成员 id 逐个 `PublishUserAuthCache`——不要用
  `invalidateUserCache`：那会连缓存里的 `Quota` 一起丢掉，而批量更新模式下数据库余额还没跟上
  缓存，重建时会读出偏高的余额。

### 八、新建渠道按凭据测试连通性

上游只有 `GET /api/channel/test/:id`，必须先落库拿到 id 才能测。管理端「新建渠道」弹窗在保存前
就要逐模型测连通性，于是加一个 `POST /api/channel/test`：

- `controller/channel_test_fork.go` —— 用请求体里的 `type/base_url/key/model/endpoint_type?/stream?` 内存构造
  `model.Channel`（`Id = 0`），复用现有 `testChannel()`。与 `POST /api/channel/fetch_models`
  同一套「不落库预览」思路。多行秘钥只取第一把（`credentialKeyFor`，与 `fetch_models` 共用同一实现）；
  key 允许为空，因为「自建算力」渠道走内网网关、上游不校验秘钥。`endpoint_type` / `stream` 与
  `GET /test/:id` 同口径透传（新建弹窗要测非 chat 端点或流式）；未知的 `endpoint_type` 落回
  `testChannel()` 默认分支（chat 端点 + OpenAI 格式），与老接口行为一致。
- `controller/channel-test.go` 五处 `// [FORK]` —— `testChannel()` 开头算一次 `preview := channel.Id == 0`，
  据此跳过定价校验（网关 `model_ratio` 与平台成本表是两套数据，不跳过会让未配价模型全部测试失败）、
  结算/消费日志（否则记到 channel 0），并在调 `RelayErrorHandler` 前把上游响应体里的明文凭据
  （含 Codex 的 OAuth token）替换成 `[REDACTED]`；`TestChannel` 的两个错误分支再对回给前端的
  message 做一次同样的脱敏——这个接口只需要 `ChannelOperate`，上游回显 Key 等于绕开
  `ChannelSensitiveWrite` 读到密钥。
- 错误 message 过 `redactCredentialError`，脱敏集合由 `credentialSecrets()` 给出（秘钥本身 +
  Codex 的 `access_token`/`id_token`/`refresh_token`），多 Key 渠道取上下文里本次实际下发的那把。
  文本规则在 `redactCredentialText()` 里（原文 + QueryEscape + PathEscape 三种形式），
  `redactCredentialError()` 只多一层「没替换到东西时原样返回原 error」——渠道测试要保留
  `*common.Message` 的 `message_key`（前端靠它翻译），上游的 `sanitizeFetchModelsError()`
  （`fetch_models` / 渠道余额）没这个需要，所以还是 `errors.New` 重包。

  **上游那份 `sanitizeFetchModelsError()` 保持原样，fork 不改它**（它所在的
  `controller/channel_upstream_update.go` 是上游高频改动的文件，为了一个无行为变化的
  重构去碰它不值得）。代价是同一套替换规则存在两份等价实现：动了其中一份的语义要同步另一份。
- `router/channel-router.go` 注册 `POST /test`，权限用 `authz.ChannelSensitiveWrite`（与 `POST /fetch_models` 一致）：
  两者都是「带鉴权头打管理员临时输入的 base_url 并回显上游错误」，对只有 `ChannelOperate` 的管理员
  等于一个有回显的内网探测能力。

**已知缺口：依赖渠道 settings 的类型测不到。** 构造出来的临时渠道只有 `type/base_url/key`，
`validateChannel()` 要求它们补齐 settings / `Other`，所以 `TaskPlugin`（缺 `task_plugin_key`）与
`VertexAI`（缺部署区域）在预览态会直接 400。`POST /fetch_models` 用
`applyTaskPluginModelPreview` / `buildAdvancedCustomModelPreviewChannel` 补齐了其中一部分；本接口
**没有**跟着补——保存后走 `GET /api/channel/test/:id` 能测，新建弹窗对这些类型不能测。
需要时再按同样的思路补，别指望它现在能覆盖全部渠道类型。

400 的 message key `type and model are required` 已登记进 7 个前端语言包（与 `Invalid parameters` 同口径）。

`testChannel()` 签名未变，既有两个调用点（`TestChannel` 与 `testChannelForHealthCheck`）零改动。

### 九、供应关系优先级（`abilities.priority` 参与选路）

**背景**：上游缓存选路只读 `channels.priority`；`abilities.priority` 虽然存在且 `GetChannel`
（缓存关闭时的 DB 路径）会读它，但开着内存缓存时它**不参与任何选路**。而 `abilities` 的主键是
`(group, model, channel_id)`，正好就是「一条供应关系」——同渠道可以给不同模型设不同优先级。

**做法**：让缓存选路改读 `abilities.priority`，缺行时逐渠道回退 `channels.priority`。

- **候选集合不动。** `group2model2channels` 仍由 `channels` 派生，`abilities` 只提供「这条供应关系
  的优先级」。不从 `abilities` 重新派生候选，是为了不引入孤儿行与停用渠道残留。
- **新增查询表** `priorityLookup`（`model/channel_priority_cache_fork.go`）：`(group, model, channel_id)
  → priority`。`InitChannelCache` 在**锁外**构建（构建要遍历全部 abilities 行，放锁内会阻塞所有选路），
  与候选列表在同一次锁内发布，二者来自同一次 DB 读取。
- **nil 语义**：行存在即采用其值，`NULL` 视作 `0`（对齐 `GetChannel`）；行不存在才回退
  `channels.priority`。所以「删掉某渠道的 abilities 行」不会把它排除出选路，只是把优先级降回渠道级。
- **写接口** `PUT /api/channel/ability/priority`（body `{model, channel_id, priority, group?}`）：
  只改一条 `(group, model, channel)` 的 abilities 行，不写 `channels` 行。`priority` 限 1–999；
  `group` 缺省为 `default`；目标行不存在返回 404。写后立即 `InitChannelCache()`，不等 3 秒 sync。
- **与 `ReorderChannelPriority` 的区别**：`POST /api/channel/priority/reorder` 是渠道级**全量重排**——
  改 `channels.priority`，并把该渠道**所有** abilities 行一起改成新值，用于调整渠道之间的整体顺序；
  `PUT /api/channel/ability/priority` 是**单条**设置，只动一个模型 × 一个渠道。想只调某个模型的优先级
  就只能用后者，用重排会把同渠道其它模型一起改掉。两者语义不同，都保留。
- **`UpdateAbilities` 保留单条 priority**（`model/ability.go`）。它原本先 `DELETE` 该渠道全部
  abilities 行再按 `channel.Priority` 重建，管理员设好的单条优先级会被「改渠道名 / 权重 / 模型列表
  / 批量打标签 / 上游模型同步」任一动作清零。现在 `DELETE` 前记下现有 priority，重建时还原，
  `enabled` / `weight` / `tag` 仍按当前渠道状态重建——它们本来就该跟着渠道走。模型列表里删掉的
  模型不会残留，重建后该行消失，维持原语义。两条还原规则：
  - **同一条供应关系按 `group|model` 还原**；
  - **`group` 变化（`default` → `vip`）时 `group|model` 键失配，保留键退化为 `model`**：同名的行
    沿用该 model 的 priority，多个旧 group 取值不同时取较大者（结果确定，且不压低已有优先级）。
    不用「新旧 group 有无交集」做条件：本产品只建单 group 渠道，一换组必然无交集，那等于换个
    说法保留静默丢失。代价是换到 priority 量级不同的 group 时旧值会被带过去，要回渠道级值用
    `PUT /api/channel/ability/priority` 单条改，或跑 `FixAbility` 重建。
- **改渠道级 priority 真的改道**，不再「返回 200 而选路不变」。保留不能是无条件的，否则
  `Channel.Update()`（`PUT /api/channel/` 带 `priority`，内置控制台的优先级 spinner / 编辑抽屉，
  `web/` 随二进制发布、可达）与 `EditChannelByTag`（带 `priority` / `models` / `group` 任一，
  三者都走重建；只有 `tag` / `weight` 才发整表 `UPDATE`）就只是改了 `channels.priority`、
  不影响选路。做法是最小侵入：`Channel` 上加非持久化的
  `PreviousPriority *int64`（`gorm:"-"`），两个调用方在写 `channels` 行**之前**把旧值填进去
  （tag 编辑按 id 逐条读，因为一条 tag 下有多个渠道、各自的旧值不同），`UpdateAbilities` 里
  priority 等于旧值的行算「沿袭渠道级」、按新 `channel.Priority` 重算，其余行算「单条覆盖」、
  原样保留。没填（nil，其余调用点）表示调用方没动渠道级 priority，一切沿用。四条回归测试都在
  `model/channel_priority_cache_fork_test.go`：`TestChannelUpdateAppliesChannelPriorityToInheritedRows`、
  `TestEditChannelByTagAppliesChannelPriorityToInheritedRows`、
  `TestEditChannelByTagPriorityOnlyKeepsOverrides`、`TestChannelGroupChangeKeepsPerModelPriority`。
  - **tag 编辑只带 `priority` 也走重建**（`shouldReCreateAbilities`）：否则 `UpdateAbilityByTag`
    一句 `UPDATE abilities SET priority` 会把这个 tag 下所有单条覆盖一起改掉（同一接口多带一个
    `models` 字段结果相反），且不走 `UpdateAbilities` 的「channel 行 → abilities 行」锁协议，与
    并发重建交错时写入会被删除/覆盖。代价是 tag 下渠道全部重建一次（`DELETE` + `INSERT`）。
  - 已知边界：**单条覆盖恰好等于渠道级值的行无法与「沿袭值」区分**，按沿袭处理——下次改渠道级
    priority 时会被一起带走。要保住这种行，就把渠道级 priority 改成别的值，或改用单条接口。
- **重建与单条优先级写入互斥**。`UpdateAbilities` 是「读旧 priority → 删 → 插」的读改写，
  `UpdateAbilityPriority` 是单条 `UPDATE`：不加锁时重建会用读到的旧值覆盖并发的写入（接口返回 200、
  优先级静默丢失），或在行被删掉的那一瞬间让写入影响 0 行、误报 404。现在两条路径都在事务里先
  `SELECT ... FOR UPDATE` 渠道行，锁序与 `AddAbility` / `DeleteAbility` 统一为「channel 行 → abilities 行」。
  SQLite 不支持该语法（`lockForUpdate` 跳过），靠它的单写者模型兜底。
- **`FixAbility`（`POST /api/channel/fix`）不保留逐条 priority。** 它与 `UpdateAbilities` 相反：
  `TRUNCATE` 后按 `channels` 用 `AddAbilities` 重建，每行 priority 取 `channels.priority`（渠道无该值时
  落 0），所以单独设过的逐条优先级会被重置，而它只存在 `abilities` 这一处、重置后无法恢复，选路
  结果随之整体改变且没有提示。这是既定边界而非待修，前提是本产品**不使用**自带控制台的「修复渠道
  一致性」按钮，BFF 也不调用该接口；仓库内唯一的自动调用是 `main.go` 启动时 `InitChannelCache`
  panic 后的一次重试（`main.go:98`），那种情况本来就要重建表与缓存。`FixAbility` 的语义是「以
  `channels` 为权威的兜底修复」，保留逐条覆盖会让「修复」不再修复被改坏的渠道级值，所以没有像
  `UpdateAbilities` 那样加 `[FORK]` 保留逻辑。若将来真要使用该入口，按「清空前快照
  `(group, model, channel_id) → priority`，重建后逐条回写」或「清空后对现存渠道改调 `UpdateAbilities`，
  只把 `AddAbilities` 用于真正的新渠道」补齐即可。

**增删单条供应关系**（`POST|DELETE /api/channel/ability`，`model/channel_ability_fork.go` 与
`controller/channel_ability_fork.go`）：

- **`channels.models` 是权威，`abilities` 是重建产物。** 直接插一行 abilities 会在两处失效：
  缓存选路的候选列表由 `channels` 派生（不读 abilities），请求根本看不到这一行；任何一次
  `UpdateAbilities` 又会先删光该渠道的 abilities 行再按 `channels.models` 重建，这一行随即消失。
  所以两个接口都以渠道的 `models` 列为写入点：新增时把模型名写进去（给了 `upstream_model`
  就同时写 `model_mapping`），删除时摘掉模型名并清掉对应映射，然后调用 `UpdateAbilities` 重建。
- **行锁**：读写 `models` 列是读改写，两个并发新增不加锁会丢一条。两条路径都先
  `lockForUpdate` 锁住渠道行，并在锁内完成去重、写列与重建。
- **group 用渠道原始 token**：`UpdateAbilities` 按 `channels.group` 的原始 token 写 group 列
  （不 trim），所以校验与回写都用原始 token，否则刚重建出来的行查不到。`PUT
  /api/channel/ability/priority` 的 group/model 也走同一个 `resolveAbilityRelation`，三个
  写接口对同一个逻辑关系用同一套口径。
- **关系是否存在只看 `channels.models`**：`abilities` 行可能正处在「已删、`models` 未改」的
  重建中间态。新增按 `models` 判重（孤儿 abilities 行不挡 400），删除也按 `models` 判存在
  （行缺失时仍摘掉模型并重建），所以 DELETE 不会对一条仍在选路的关系回 404。
- **重复新增返回 400**（`The supply relation already exists`），不静默成功；删除不存在的行返回 404。
- **已知边界：单条增删只对单 group 渠道语义正确，多 group 渠道会连带影响其它 group。**
  `channels.models` 是渠道级而不是 group 级的列，`abilities` 又按它重建，所以两处都不是 group 级：
  - 新增（`POST /api/channel/ability`）：请求 `group=vip` 时模型名照样写进 `channels.models`，
    重建会给该渠道**每个 group** 各插一行。请求里那条（`vip`）的 priority 用 body 传入值，其它
    group（如 `default`）的新行取 `channels.priority`——原本不服务该模型的 `default` 也可能开始
    被选路。
  - 删除（`DELETE /api/channel/ability`）：摘掉模型名后，其它 group 下的同名行一起消失——与
    「在渠道接入改模型清单」的行为一致。

  两条路径的响应都只回请求里那一行，不返回实际受影响的 group，调用方无法从结果判断影响范围。
  **本产品只建单 group 渠道**（BFF 也固定写 `default`，见本节末的已知边界），所以不为此改成
  group 级模型清单，也不在接口里拒绝多 group 渠道。若将来允许建多 group 渠道，必须二选一：改成
  group 级模型清单，或在这两个接口里显式拒绝 group 数 > 1 的渠道（400），并补一条 `default,vip` 渠道的回归测试，断言只影响请求的 group。

**已知边界**：BFF 的写接口固定落 `default` 组；查询表本身不做 group 过滤，但部署里若存在多 group
渠道，单条优先级目前只对 `default` 组生效。

### 十、模型标签筛选（`c20679b0c` + `7a45e2f54`）

**背景**：模型列表页要从「标签」筛模型。上游 `/api/models/search` 只有一个 `keyword`，它把
`model_name` / `description` / `tags` 三列 OR 起来模糊匹配——按标签筛会连模型名、描述里
碰巧出现该词的行一起返回。

**做法**：

- **搜索加一格 `tag`**（`model/model_meta.go` 的 `SearchModels` / `SearchModelsWithChannels`、
  `controller/model_meta.go`）：只匹配 `tags` 一列，**子串匹配**，与 keyword 对 tags 的口径一致
  （取舍见「已知限制」）。`GET /api/models/`（`GetAllModelsMeta`）不带这个筛选，恒传空串。
- **合成行（`id=0`，「渠道里有、还没建元数据」）的两个口径**：带 `tag` 时不追加（合成行没有
  tags）；`status=disabled`（列表页的「未上架」）时**追加**——合成行的 `status` 零值就是 0，
  上游原本只在完全不带筛选时才追加，导致「未上架」这一页整批为空，而这正是该页最主要的
  操作对象。

**已知边界**：`tag` 是子串匹配，不做逗号边界匹配。大小写不敏感（`LOWER(tags) LIKE ?`，
与标签字典接口同一口径）。

**文件**：`model/model_meta.go`、`controller/model_meta.go`（上游文件，`[FORK]` 标记）、
`controller/model_management_test.go`（`tag_filtering` 与 `status=disabled` 用例）。

### 十一、模型标签字典接口（`7cd06ea44`）

**背景**：列表页改成服务端分页后，筛选框的标签候选只能从当页 20 条模型的 `tags` 里现算。
命中超过 20 条时，只在第 21 条之后出现的标签就看不到建议了（手打仍能筛到，只是没提示）。

**做法**（`GET /api/models/tags?keyword=&limit=`，`model/model_tag_fork.go` +
`controller/model_tag_fork.go`）：`models.tags` 是 `varchar(255)` 逗号串，没有独立标签表，
所以把列拉回来在内存里拆：按小写去重（按 `id` 序保留首次出现的原样大小写，因此哪种拼写
可复现）、`sort.Strings`（码点序，不是拼音序）、默认上限 50 条。带 `keyword` 时先让 DB 按子串
粗筛整格，再在内存按「单个标签含该词」收窄——否则 `vision,旗舰` 这种整格命中会让不相关的
标签冒出来。权限 `model:read`。

**大小写口径**：`SearchModels` 的 tag 筛选与这里的粗筛都用 `LOWER(tags) LIKE ?`（传入值
`ToLower`），所以 PG 上也大小写不敏感。字典按小写去重只留一种拼写，而筛选不区分大小写，
同一标签同时存在 `Vision` / `vision` 时点选任一拼写都能筛到全部变体。手打大小写和库里不一致
同样能拿到候选。

**文件**：`model/model_tag_fork.go`、`controller/model_tag_fork.go`、
`controller/model_tag_fork_test.go`（含 SQLite/MySQL/PG 三库用例，后两库需 `TEST_MYSQL_DSN` /
`TEST_POSTGRES_DSN`）；路由注册在 `router/api-router.go` 与
`middleware/access_token_routes.go`（两处都是单行 `[FORK]`）。

### 十二、失败调用的调用状态落库（`fork/feat/log-call-status-on-main`）

**背景**：上游 usage log 只覆盖走到计费的成功调用。以下失败此前在控制台完全看不到：中间件前置
校验/鉴权拒绝（模型未上架、无可用渠道、密钥无权限、密钥不可用、渠道被禁）、`controller/relay.go`
的本地请求校验失败、以及预扣费/额度不足——后者显式带 `ErrOptionWithNoRecordErrorLog`，被
`RecordRelayErrorLog` 主动跳过。另有一类虽落库但 `error_code` 无诊断价值：无可用渠道统一记
`model_not_found`，上游 401/429 只记 `bad_response_status_code`，无法区分「模型未上架」与
「有模型但没渠道」，也无法区分「用户密钥不可用」与「上游限速」。

**做法**：

- **中间件落库**（`middleware/utils.go`）：`abortWithOpenAiMessage` 末尾补记一条错误日志。只覆盖
  「已认证用户、可归类的客户端失败」——`userId == 0`（未认证）与 429（本地模型限流，
  `middleware/model-rate-limit.go`）不写，避免日志放大；调用方未给码时按状态码推导
  （401→`invalid_token`、403→`access_denied`、其余 4xx→`invalid_request`），推导不出用户可见
  状态的 5xx 也不写（带显式码的 5xx，如模型未上架，仍写）。先 `RecordRequestPolicyTermination`
  再 `RecordRelayErrorLog`，否则落库内容缺这次失败对应的策略决策。
- **日志 content 固定英文**（`middleware/utils.go`）：错误日志的 `content` 必须与读者语言无关，
  否则同一次失败在不同语言的请求里落下不同文本，检索与聚合都对不上。为此把
  `abortWithOpenAiMessage` 拆成 `abortWithLogMessage(c, status, message, logMessage, code...)`
  分发层，并新增 `abortWithI18nMessage(c, status, key, params, code...)`：响应用
  `i18n.T(c, key, params)` 保持读者语言，落库用 `i18n.Translate(i18n.LangEn, key, params)` 取同一
  key 的英文句子。`middleware/distributor.go` / `auth.go` / `task_plugin.go` 共 17 处本地化调用点
  迁到 `abortWithI18nMessage`；本来就是英文句子的调用点（`abortWithOpenAiMessage`）行为不变；
  429 那 4 处不落库，有意不迁移。
- **非中间件的失败补记**（`controller/relay.go`）：本地请求校验失败补一条 `RecordRelayErrorLog`；
  `PrepareRequestBilling` 失败走新增的 `ForceRecordRelayErrorLog`（与 `RecordRelayErrorLog` 同在
  `service/relay_error.go`），它清除 `ErrOptionWithNoRecordErrorLog` 抑制标记后落库，保留
  `error_type` / `error_code` / `status_code` 等全部信息（仍受 `constant.ErrorLogEnabled` 开关控制）。
  额度类失败（`insufficient_user_quota` / `pre_consume_token_quota_failed`）的文案是 billing 层按
  读者语言生成的，落库前换成固定英文句子（`Insufficient user quota` /
  `Not enough quota to pre-consume for this request`）；只换落库副本的文案，原错误对象不变、
  响应仍是读者语言，与上面「日志 content 固定英文」同一口径。
- **定价失败落库文案**（`relay/helper/price.go` / `relaykit/types/error.go`）：`model_price_error` 的
  文案由 `i18n.T(c, ...)` 按读者语言生成（`relay.model_price_not_configured`（含 admin 变体）/
  `relay.task_usage_price_unsupported` / `relay.shared_task_price_needs_override`）。生成处用新增的
  `types.ErrOptionWithLogMessage(i18n.Translate(i18n.LangEn, key, params))` 把同一 key 的英文句子
  作为「落库专用文案」带上（`NewAPIError.logMessage` / `GetLogMessage`；`types.NewError` 包装时经
  `errors.As` 保留），`RecordRelayErrorLog` 落库前优先用它写 `content`——额度类走
  `ForceRecordRelayErrorLog` 的按码替换，两条路径都不改响应文案，理由同上。
- **错误码归一**（`relaykit/types/error.go`）：删 `ErrorCodeModelNotFound`，新增
  `model_not_listed` / `no_available_channel` / `get_channel_failed`（渠道选择路径一分为三）与
  `invalid_token` / `upstream_unauthorized` / `upstream_rate_limited`（状态码专属码）；新增
  `GetUpstreamErrorCode` / `SetErrorCode` / `SetRecordErrorLog` 三个方法。
- **渠道选择定码**（`service/channel_select.go`）：`modelFailureCode` 在错误路径上查一次模型目录
  （`model.IsModelListed`）：已上架→`no_available_channel`，未上架→`model_not_listed`；取渠道
  本身出错→`get_channel_failed`。目录查询失败**不**臆断「未上架」（那是把数据库故障当成用户配置
  问题），改记 `common.SysError` 并退回 `no_available_channel`。
- **上游 401/429 定码**（`service/error.go`）：`RelayErrorHandler` 拆出 `relayErrorHandlerRaw`，
  在结果上按上游状态码覆盖 `error_code`（401→`upstream_unauthorized`、429→
  `upstream_rate_limited`），**不改 `StatusCode`**；新码不带 `channel:` 前缀，`IsChannelError`
  判假，管理员配置的按状态码重试/禁用策略照常生效。
- **保留上游具体原因**（`service/relay_error.go`）：覆盖码会丢掉 provider 的具体原因
  （`invalid_api_key` / `insufficient_quota`），故在 `other.admin_info.upstream_error_code` 另存
  一份，仅当上游码非空且不同于归一码时写。
- **模型目录优先级**（`model/model_meta.go`）：新增 `IsModelListed`，复用 `resolveModelMetadata`
  的目录优先级（exact → prefix → suffix → contains），只认启用行；`resolveModelMetadata` 补
  `Order("id")` 作 tie-break，同规则多行命中时结果稳定。
- **错误日志带模型名**（`middleware/distributor.go`）：`original_model` 原本只在成功选路后写入，
  提前到选路前写，让其后的前置校验失败日志也带得上模型名（成功路径稍后用同值覆盖，无副作用）。

**对外契约变更声明（重要）**：「无可用渠道」失败此前是 HTTP **503** + `code: "model_not_found"`，
现在**状态码仍是 503**，`code` 拆成三个：`model_not_listed`（模型未上架）、`no_available_channel`
（已上架但无可用渠道）、`get_channel_failed`（取渠道报错）——三条失败路径在
`service/channel_select.go` 都返回 `http.StatusServiceUnavailable`。**依赖旧 `model_not_found`
的调用方需同步**。401/429 的**响应码不是**变更——`ToOpenAIError()` 对 `ErrorTypeOpenAIError`
分支返回原始上游 RelayError，客户端仍收到上游自己的码；归一码只写进 usage log 的 `error_code`。

**残留 `model_not_found` 字面量（确认无需清理）**：`types.ErrorCodeModelNotFound` 常量已删、全仓
无引用（`grep -rn "ErrorCodeModelNotFound" --include="*.go" .` 零命中）；字符串字面量仍存两处，
都在本分支未改动的文件、属不同路径：

- `controller/model.go` 的 `RetrieveModel`（`GET /v1/models/:model`，OpenAI 兼容接口）返回
  `"model_not_found"`——是 OpenAI 契约字面量，与错误码归一无关，保留；
- `pkg/perf_metrics/outcome.go` 的 `classifyFailure` 把它当**上游码**归类，与新码一样落
  `OutcomeFailure`，无回归。

**控制台展示（已知限制）**：错误日志的 `content` 是已渲染的英文句子（`model` / `group` 参数已代入）。
前端用量日志对错误日志走 `renderLogContent(other, t) ?? log.content` 回退，而错误日志路径
（`model.RecordErrorLog`）不写 `content_parts`、前端也不按 `error_code` 本地化，因此控制台展示的
是英文原文。本 fork 不改前端，接受这一口径：日志是运维产物，固定英文句子利于跨语言检索。

**`[FORK]` 标记**：本分支最初直接改上游方法体、未加标记，与其余十一组定制的约定不符；
已按「每处独立改动点一条」补齐——在 8 个上游文件里新增 17 处 `// [FORK]`，全仓计数由 191
升到 213。只标改动块、不给整个函数加标记，与既有 191 处同口径；像「响应文案随读者语言、
落库固定英文」这类在每个调用点都成立的约定，只在定义处（`middleware/utils.go`、
`service/channel_select.go`）标一次，不逐调用点重复；测试文件不加标记（改动靠跑用例保证）。

**文件**：`middleware/utils.go`、`middleware/distributor.go`、`middleware/auth.go`、
`middleware/task_plugin.go`、`controller/relay.go`、`model/model_meta.go`、
`relaykit/types/error.go`、`service/channel_select.go`、`service/error.go`、
`service/relay_error.go`；测试
`middleware/utils_fork_test.go`（新增）、`middleware/distributor_test.go`、
`controller/relay_error_log_test.go`、`service/error_test.go`、`model/model_meta_fork_test.go`（新增）、
`router/relay_router_test.go`。

**三库实跑记录**（2026-11-26）：

- 环境：Go `go1.26.11 darwin/arm64`；SQLite（测试用 `:memory:` 内存库）；MySQL `8.0.46`
  （`mysql:8.0`，`127.0.0.1:13306`）；PostgreSQL `16.15`（`postgres:16-alpine`，
  `127.0.0.1:15432`）。
- 命令：

  ```bash
  TEST_MYSQL_DSN='root:testpass@tcp(127.0.0.1:13306)/newapi_cr_test?charset=utf8mb4&parseTime=True&loc=Local' \
  TEST_POSTGRES_DSN='postgres://tm:tm_dev_password@127.0.0.1:15432/newapi_cr_test?sslmode=disable' \
  go test ./middleware/ ./service/ ./model/ ./controller/ ./router/ -count=1
  ```

- 结果：`middleware`、`service`、`router` 全 PASS；`model`、`controller` 的 **MySQL 子测试**
  FAIL，SQLite 与 PostgreSQL 子测试全 PASS。定稿修复（`abortWithLogMessage` 分发层、
  `ForceRecordRelayErrorLog` 归位 `service/relay_error.go`、`IsModelListed` 的 `Order("id")`）
  后在同一工作树复跑，逐包结果与 MySQL 失败点与上表完全一致；`go build ./...` 与
  `relaykit` / `tokenkit` 的 `GOWORK=off go build ./...` 均通过。
- MySQL 失败是**既有限制、与本分支无关**：共 12 处（`model` 1 处 + `controller` 11 处），报错一致为
  `Error 1170 (42000): BLOB/TEXT column 'phone' used in key specification without a key length`，
  源自 `model/user.go` 的 `Phone *string ... uniqueIndex:uk_users_phone`——MySQL 把 `*string` 渲染成
  `TEXT`，唯一索引需要 key length（PG / SQLite 无此约束）。该 tag 在本分支未改动
  （`git diff fork/main...HEAD -- model/user.go` 为空），并在 `fork/main` 的干净 worktree 上以同一 DSN
  复现同一报错（`go test ./model/ -run TestAuthCacheIntegrityDatabaseMatrix`：`/mysql` FAIL，
  `/sqlite`、`/postgres` PASS）。与上面「数据库」一节的「MySQL 不在运行范围」一致。
- `relaykit` / `tokenkit` 独立构建：各自 `GOWORK=off go build ./...` 通过。

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

共 114 个文件（64 个上游文件被改、50 个新增；不含 `fork/` 目录与 `.github/`）。
后端 `.go` / `.sql` 部分 +10302/−229，`web/` 部分 +145/−0（都是 i18n 与类型联合）。
（定稿修复把分支新增的 `service/relay_error_fallback.go` 并回 `service/relay_error.go` 并删除，
故新增数比本分支初版少 1。）

> 统计口径：`git diff --name-status 6ef123cd5..HEAD -- . ':(exclude)fork' ':(exclude).github'`。
> 上游基线 `6ef123cd5` 之后还删了 6 个上游的 `.github/workflows/*.yml`、只留 `quay-publish.yml`，
> 那不是定制，不计入上面的文件数。64 个被改文件里有 9 个是 `web/`、4 个不是功能
> 代码：`AGENTS.md`（写明部门表只支持 PG 的既定例外）、`docs/authentication.md`、`go.mod` / `go.sum`（依赖）。

**上游文件（merge 时唯一可能冲突的地方）**

| 文件 | 改动 | 风险 |
|---|---|---|
| `model/user.go` | 不变量辅助函数、`UpdateWithTx`/`delete` 的守卫、`LastCalledAt` / `DepartmentId` / `Phone` 字段、三条扣费写入路径、`SearchUsers` 的筛选条件 | **最高**，前四组定制都改它 |
| `controller/user.go` | `CreateUser` 允许 root 建 root、接受部门与手机号；`ManageUser` 删两处硬拒、新增两个 action、扩写 demote 分支；`SearchUsers` 加筛选 | **高** |
| `model/channel_cache.go` | 新增 `priorityLookupCache` 的构建与发布、`GetRandomSatisfiedChannel` 的优先级改读 `abilities.priority`（5 处 `[FORK]`） | **高**，逐请求热路径 |
| `router/api-router.go` | 五处插路由：`/api/department/`、`PUT /api/user/:id/department`、`GET /api/data/departments`、`POST /api/user/batch/reset_password`、`GET /api/models/tags` | 中，定制都插在同一文件 |
| `model/ability.go` | `UpdateAbilities` 在 `DELETE` 前记下现有 abilities 行的 priority，重建时按 `group|model`（换 group 时退化到 `model`）还原，并用 `Channel.PreviousPriority` 让改渠道级 priority 生效（见「九」，3 处 `[FORK]`） | 中，8 个调用点的高频路径 |
| `model/channel.go` | `Channel` 加非持久化字段 `PreviousPriority`，`Update()` / `EditChannelByTag` 在写 `channels` 行前把旧 priority 填进去，tag 编辑带 priority 时改走重建（4 处 `[FORK]`） | 中，渠道编辑主路径 |
| `controller/audit.go` | 审计 action 英文模板 map 加 5 项 | 低 |
| `service/security_verification.go` | 新增 1 个 proof scope 常量，白名单与「要求表」各加 1 项 | 低 |
| `router/channel-router.go` | 权限路由表插入两条：`PUT /ability/priority`（`ChannelWrite`）与 `POST /test`（`ChannelSensitiveWrite`，1 处 `[FORK]`） | 低，单行 |
| `router/channel_router_test.go` | 同上两条路由的权限断言 | 低 |
| `controller/channel-test.go` | `testChannel()` 按 `channel.Id == 0` 跳过定价校验与结算/消费日志、错误日志脱敏，`TestChannel` 回前端前脱敏（5 处 `[FORK]`） | 中，逐模型测试热路径 |
| `controller/channel.go` | `FetchModels` 的取 key 逻辑改为调 `credentialKeyFor`（1 处 `[FORK]`） | 低，单行 |
| `controller/channel_test_internal_test.go` | 补 `testChannel()` 预览态用例（跳过定价/消费日志、日志脱敏） | 低 |
| `service/access_token_scope.go` | proof scope → PAT scope 表新增 `admin.user.password.batch_reset` → `user:write` | 低 |
| `middleware/access_token_routes.go` | PAT 路由规则新增 `POST /api/user/batch/reset_password` → `user:write`、`GET /api/models/tags` → `model:read` | 低 |
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
| `model/model_meta.go` | `SearchModels` / `SearchModelsWithChannels` 加 `tag` 参数（只匹配 `tags` 列、子串）；合成行带 `tag` 时不追加、`status=disabled` 时追加（2 处 `[FORK]`）；新增 `IsModelListed`、`resolveModelMetadata` 补 `Order("id")`（见「十二」） | 中，模型列表主查询 |
| `controller/model_meta.go` | `listModelsMeta` 多一个 `tag` 透传，`GetAllModelsMeta` 恒传空串（2 处 `[FORK]`） | 低 |
| `controller/model_management_test.go` | `tag_filtering` 用例；`status=disabled` 保留合成行的断言 | 低 |
| `constant/context_key.go` | 新增 `ContextKeyUserDisplayName` / `ContextKeyUserDepartmentName` | 低 |
| `middleware/auth.go` | 鉴权通过后把身份写进上下文；错误响应迁到 `abortWithI18nMessage`（响应本地化、落库英文，见「十二」） | 低 |
| `middleware/distributor.go` | 分发后把渠道名/供应商写进上下文；`original_model` 提前写入、selectErr 走 `abortWithLogMessage` 落库、`noAvailableChannelMessage` 返回响应文案与日志英文两份（见「十二」） | 低 |
| `middleware/utils.go` | `abortWithOpenAiMessage` 拆成 `abortWithLogMessage` 分发层 + `abortWithI18nMessage`；abort 时补记错误日志（见「十二」） | 中，所有 abort 路径 |
| `middleware/task_plugin.go` | 1 处本地化 abort 迁到 `abortWithI18nMessage` | 低，单行 |
| `relaykit/types/error.go` | 删 `ErrorCodeModelNotFound`；新增 `model_not_listed` / `no_available_channel` / `get_channel_failed` / `invalid_token` / `upstream_unauthorized` / `upstream_rate_limited` 与 `SetErrorCode` / `GetUpstreamErrorCode` / `SetRecordErrorLog`（见「十二」） | 中，错误码是跨模块契约 |
| `service/channel_select.go` | `modelFailureCode` 在失败路径按 `model.IsModelListed` 区分未上架 / 无可用渠道 / 取渠道失败 | 中，选路失败路径 |
| `service/error.go` | `RelayErrorHandler` 拆出 `relayErrorHandlerRaw`，按上游 401/429 覆盖 `error_code`（不改 `StatusCode`） | 中，上游错误解析主路径 |
| `service/relay_error.go` | 落库 `error_code` 并另存 `upstream_error_code`；新增 `ForceRecordRelayErrorLog`（见「十二」） | 中 |
| `controller/relay.go` | 本地请求校验失败补 `RecordRelayErrorLog`；`PrepareRequestBilling` 失败走 `ForceRecordRelayErrorLog` | 中，relay 主路径 |
| `middleware/distributor_test.go`、`service/error_test.go`、`router/relay_router_test.go`、`controller/relay_error_log_test.go` | 对应测试：双语 abort、401/429 覆盖码、路由错误码、强制落库绕过抑制 | 低 |
| `service/task_billing.go` | `LogTaskConsumption` 加一行调用写指纹（任务型渠道的提交日志） | 低 |
| `relay/responses_websocket.go` | lockedContext 白名单提成包级变量 `lockedContextKeys`（行为不变，为让白名单可被测试断言） | 低 |
| `controller/token.go`、`model/token.go` | 令牌「使用方」`owner`：请求字段（指针区分未传）、128 字节校验、写库 | 低 |
| `controller/token_test.go`、`controller/admin_user_verification_test.go`、`relay/responses_websocket_test.go`、`service/task_billing_test.go` | 对应测试：owner 用例、批量重置的 proof 前置断言、白名单断言、指纹断言 | 低 |
| `web/src/i18n/locales/{en,zh,zh-TW,ja,ru,vi,fr}.json`、`web/src/i18n/static-keys.ts` | 新增错误文案的翻译键（部门、手机号、批量重置、单条优先级、令牌使用方等），`static-keys.ts` 同步白名单 | 低 |
| `web/src/features/auth/secure-verification/types.ts` | `SecurityProofScope` / `VerificationOperation` 联合类型加 `admin.user.management` 及 context | 低 |

**本仓库新增（文件名带 `_fork` 后缀，上游不可能改，merge 时不会冲突）**

| 文件 | 内容 |
|---|---|
| `model/department_fork.go` | `Department` 模型、名字归一与校验、`LockDepartmentWithTx` / `EnsureDepartmentExistsWithTx` |
| `model/user_phone_fork.go` | `IsPhoneDuplicate`：只认 `uk_users_phone` 唯一索引冲突（PG 看约束名、SQLite 看错误文本） |
| `controller/user_phone_fork.go` | `normalizePhoneFork` 归一与 11 位校验、`ensurePhoneFreeWithTx` 事务内预检、两条 `common.Message` 错误 |
| `model/goose_fork.go` | 内嵌 PG 迁移的 goose 集成：`Provider.Up()`、非 PG 库直接拒绝 |
| `migrations/embed.go` | 内嵌 `migrations/postgres/*.sql`，导出 `migrations.Postgres` |
| `migrations/postgres/20261117160000_create_department.sql` | 部门表建表迁移（仅 PG） |
| `controller/department_fork.go` | 四个 CRUD handler + `PUT /api/user/:id/department` + `isDuplicateKey` |
| `controller/department_usage_fork.go` | 部门用量聚合 handler |
| `controller/user_management_fork.go` | 编辑用户三项合一（角色 / 部门 / 手机号）的 profile 更新逻辑 |
| `controller/department_fork_test.go`、`controller/department_usage_fork_test.go`、`controller/user_department_fork_test.go`、`controller/user_multi_root_fork_test.go`、`controller/user_search_department_fork_test.go`、`controller/user_phone_fork_test.go`、`controller/user_unified_update_fork_test.go` | controller 包测试 |
| `model/department_fork_test.go`、`model/user_department_fork_test.go`、`model/user_last_called_fork_test.go`、`model/user_multi_root_fork_test.go`、`model/user_search_filter_fork_test.go`、`model/user_phone_fork_test.go` | model 包测试 |
| `service/channel_key_fingerprint_fork.go` | 上游密钥指纹计算与写入 admin_info |
| `service/channel_key_fingerprint_fork_test.go` | 指纹向量表（与 BFF 侧共用）、写入路径测试 |
| `service/security_verification_fork.go` | `admin.user.manage` 的 proof 校验规则 |
| `model/log_identity_snapshot_fork_test.go` | 身份快照的写入/读取侧回归（上下文、后台路径、删除后列表） |
| `model/user_cache_fork_test.go` | 缓存往返、`updateUserCache`、冷启动返回值、Redis 关闭时不查部门 |
| `model/usedata_fork_test.go` | `quota_data` 按身份快照分桶、同键累加的回归 |
| `common/random_password_fork.go` | 8 位随机账号密码：三类字符必现、Fisher–Yates 打散位置、`crypto/rand` 无偏 |
| `model/user_batch_password_fork.go` | `LockUserForBatchPasswordResetWithTx`（Unscoped 行锁，软删行也要锁得到） |
| `controller/user_batch_password_reset_fork.go` | 批量重置 handler + 事务体 `resetBatchPasswordsInTx`（锁内二次授权在那里） |
| `service/security_verification_batch_password_fork.go` | proof context 定义、id 集合校验（≤100、升序、不许重复） |
| `model/channel_priority_cache_fork.go` | 优先级查询表 `priorityLookup`、`buildPriorityLookup`、`priorityOf`、`effectivePriority` |
| `model/channel_priority_fork.go` | 单条优先级写接口：1–999 校验、两条 `common.Message` 错误、`UpdateAbilityPriority` |
| `controller/channel_priority_fork.go` | `PUT /api/channel/ability/priority` handler，写后 `InitChannelCache()` |
| `model/channel_ability_fork.go` | 增删单条供应关系：以 `channels.models` 为权威写列、同步 `model_mapping`、重建 abilities |
| `controller/channel_ability_fork.go` | `POST` / `DELETE /api/channel/ability` handler，写后 `InitChannelCache()` |
| `model/channel_ability_fork_test.go`、`controller/channel_ability_fork_test.go` | 增删关系、映射同步、重复 400、写后可选路 |
| `controller/channel_test_fork.go` | `POST /api/channel/test` handler：按凭据构造临时渠道、复用 `testChannel()`、错误脱敏 |
| `controller/channel_test_fork_test.go` | handler 级用例：入参 400、空秘钥放行、上游失败不回 HTTP 错、密钥不回显、多行 key 口径、`endpoint_type`/`stream` 透传 |
| `controller/user_batch_password_reset_fork_test.go` | controller 包测试（含锁内二次授权与整批回滚） |
| `model/channel_priority_cache_fork_test.go` | 选路基线、查询表、逐渠道回退、缓存开关一致性、`UpdateAbilities` 保留优先级、改渠道级 priority / tag 编辑（含只带 priority）/ 换 group 四条回归 |
| `model/channel_priority_fork_test.go` | `UpdateAbilityPriority` 只改目标行、范围校验、缓存版本推进 |
| `controller/channel_priority_fork_test.go` | 端点用例（含缺省 group、错误码、写后立即生效） |
| `model/model_tag_fork.go` | `ListModelTags`：拉 `tags` 列拆开去重（小写比较、保留原样大小写）、码点序、默认上限 50 |
| `controller/model_tag_fork.go` | `GET /api/models/tags` handler |
| `controller/model_tag_fork_test.go` | 去重排序、`keyword` 按单个标签收窄（不是整格）、`limit` 与缺省 |
| `middleware/utils_fork_test.go` | abort 落库回归：`abortWithOpenAiMessage` / `abortWithI18nMessage` 的响应与落库文本、`userId==0` 与 429 不落库、带显式码的 5xx 仍落库（见「十二」） |
| `model/model_meta_fork_test.go` | `IsModelListed` 目录优先级（exact 优先、disabled、同名多行 tie-break 稳定）与无库时不报错（见「十二」） |

（另有 12 个上游文件被改签名、补迁移或补测试，已并入上表。）上游若在这些路径放了同名文件，
git 会报 `add/add` 冲突——这是唯一需要留意的情形。

`model/user.go` 里有一段 `User` 结构体的整段对齐格式化 diff（加字段导致列宽变化），属噪声，
不影响行为，但会让该文件的 diff 看起来比实际改动大。

## 标记约定

每处改动或紧邻的一组改动上方（只标改动块，不给整个函数加标记）：

```go
// [FORK] <一句话说明为什么这块和上游不同>
```

当前共 208 处、66 个文件（含本分支新增的 17 处，见「十二」）：

| 文件 | 处数 |
|---|---|
| `model/user.go` | 17 |
| `controller/user.go` | 16 |
| `model/channel_priority_cache_fork_test.go` | 13 |
| `model/channel_ability_fork.go` | 9 |
| `model/channel_ability_fork_test.go` | 7 |
| `service/security_verification.go` | 7 |
| `controller/channel-test.go` | 6 |
| `controller/department_fork.go` | 6 |
| `model/department_fork.go` | 6 |
| `model/user_last_called_fork_test.go` | 6 |
| `model/channel_cache.go` | 5 |
| `model/channel_priority_cache_fork.go` | 5 |
| `model/channel.go` | 5 |
| `model/usedata.go` | 5 |
| `router/api-router.go` | 5 |
| `controller/channel_ability_fork.go` | 4 |
| `model/channel_priority_fork.go` | 4 |
| `model/log.go` | 4 |
| `model/model_meta.go` | 4 |
| `relaykit/types/error.go` | 4 |
| `constant/context_key.go` | 3 |
| `middleware/distributor.go` | 3 |
| `model/ability.go` | 3 |
| `router/channel-router.go` | 3 |
| `controller/audit.go` | 2 |
| `controller/channel_ability_fork_test.go` | 2 |
| `controller/channel_priority_fork_test.go` | 2 |
| `controller/channel_priority_fork.go` | 2 |
| `controller/model_meta.go` | 2 |
| `controller/model_tag_fork_test.go` | 2 |
| `controller/relay.go` | 2 |
| `middleware/access_token_routes.go` | 2 |
| `middleware/auth.go` | 2 |
| `middleware/utils.go` | 2 |
| `model/channel_priority_fork_test.go` | 2 |
| `model/department_fork_test.go` | 2 |
| `model/user_auth_cache.go` | 2 |
| `service/channel_select.go` | 2 |
| `service/relay_error.go` | 2 |
| `service/security_verification_fork.go` | 2 |
| `common/random_password_fork.go`、`controller/channel.go`、`controller/channel_test_fork.go`、`controller/department_usage_fork.go`、`controller/model_tag_fork.go`、`controller/security_enrollment_test.go`、`controller/user_batch_password_reset_fork.go`、`controller/user_management_fork.go`、`controller/user_unified_update_fork_test.go`、`migrations/embed.go`、`model/goose_fork.go`、`model/main.go`、`model/model_tag_fork.go`、`model/redemption.go`、`model/user_batch_password_fork.go`、`model/user_cache.go`、`model/user_department_fork_test.go`、`model/user_phone_fork.go`、`relay/responses_websocket.go`、`service/access_token_scope.go`、`service/channel_key_fingerprint_fork.go`、`service/error.go`、`service/log_info_generate.go`、`service/security_verification_batch_password_fork.go`、`service/task_billing.go`、`service/violation_fee.go` | 各 1 |

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

用仓库自带的入口（`.github/workflows/ci.yml` 已在 fork 中删除，只保留 `quay-publish.yml`，
所以 CI 不再跑测试，下面的命令靠本地 / 人工执行）：

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

> 源码里七组定制（多 root、最近调用、部门、手机号、上游密钥指纹、批量重置密码、供应关系优先级）
> 恒在，tag 不编码定制组，也不跟随上游版本号。
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
- `PUT /api/channel/ability/priority`：按供应关系设优先级。body
  `{"model": "...", "channel_id": <id>, "priority": 1..999, "group": "default"}`，`group` 可缺省。
  只改 `(group, model, channel_id)` 这一条 abilities 行，不写 `channels`；返回
  `{"model", "channel_id", "priority"}`。权限 `ChannelWrite`。`priority` 越界 → 400
  （`Priority must be between 1 and 999`），目标行不存在 → 404（`The supply relation does not exist`）。
  写后立即刷新路由缓存，不需等 3 秒 sync。
- `POST /api/channel/priority/reorder` 仍按渠道级全量重排（`channels.priority` + 该渠道全部 abilities
  行），与上面这条的语义区别见「定制清单 · 九、供应关系优先级」。
- `POST /api/channel/ability`：新增一条供应关系。body
  `{"model": "...", "channel_id": <id>, "group"?: "default", "upstream_model"?: "...", "priority"?: 1..999}`。
  `upstream_model` 与 `model` 不同时写成 `channels.model_mapping`，`priority` 缺省为 1。
  已存在同一 `(group, model, channel_id)` → 400（`The supply relation already exists`）；
  渠道不存在 → 404；`(group, model, channel)` 重建后仍无行 → 404。
- `DELETE /api/channel/ability`：删除一条供应关系。body `{"model": "...", "channel_id": <id>, "group"?: ...}`。
  从 `channels.models` 摘掉模型名、清掉对应映射并重建 abilities；行不存在 → 404。
  两者权限均为 `ChannelWrite`，写后立即刷新路由缓存。

- `GET /api/models/search` 多了 `tag` 参数：只按 `tags` 列筛（子串匹配，`%`/`_` 未转义），
  与 `keyword` 互不替代，可同时用。同时带 `tag` 时不返回合成行（`include_channel_models=true`
  下）；`status=disabled` 时返回合成行（列表页「未上架」），见「定制清单 · 十」。权限 `model:read`。
- `GET /api/models/tags?keyword=&limit=`：标签字典，返回去重后按码点排序的标签全集，`limit`
  缺省 50，`keyword` 按「单个标签含该词」收窄（不是整格匹配）。权限 `model:read`，
  见「定制清单 · 十一」。
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
4. **前端渲染**：本 fork 对 `web/` 只改了 i18n 语言包、`static-keys.ts` 白名单与 scope 联合类型，
   没有页面组件接线。若在自己前端展示这些字段/接口，属另一侧的验证。
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
| 新建用户的默认侧边栏配置不含 admin 区，事后 `grant_root` 不重算 | 只影响上游自带控制台；本 fork 的 `web/` 改动只有 i18n 与类型，没动侧边栏逻辑。 |
| 模型标签筛是子串匹配、且 PG 上区分大小写 | `tag=视觉` 会连 `视觉理解` 一起筛出来；`tag=%` 等价于「`tags` 非空」（`%`/`_` 未转义）。改成逗号边界匹配又要处理库里 `a, b` 这类带空格的写入，代价大于收益；候选下拉（`GET /api/models/tags`）给的是精确标签，点选不会误中，手打才会。 |
| 极端情况下仍可能被人工操作清空 root（手工 SQL、迁移脚本） | 事故兜底：把可信账号 `UPDATE users SET role=100, status=1`，或删除 setups 记录后重启走初始化。**仅事故时人工执行，不可当控制手段。** |

## 许可证与合规

上游是 **AGPLv3**（`LICENSE`），并启用了 §7 附加条款（`NOTICE`）：修改版必须保留署名
`Frontend design and development by New API contributors.`，并在界面显著位置保留指向
<https://github.com/QuantumNous/new-api> 的可见链接。本 fork 未改动这些标识。

AGPLv3 的网络条款意味着：通过网络向用户提供服务时，有义务提供**修改后的完整源码**。因此
本 fork 不能私有，必须可获取。若不愿承担该义务，上游的商业授权联系人是 `support@quantumnous.com`。

**状态：需法务确认，上线前必须落定。**
