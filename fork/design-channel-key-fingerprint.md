# 日志记录上游密钥指纹 — 设计方案

## 意图

日志目前只能靠 `other.admin_info.multi_key_index`（密钥在渠道密钥数组里的**位置**）判断
「这次调用用了哪把上游密钥」。位置不是稳定标识：渠道里增删或重排密钥后，同一序号指向另一把
钥匙，历史日志的掩码、`/reveal` 的明文、以及「按密钥明文反查日志」的结果都随之漂移。

本方案给日志补一个**密钥指纹**：写日志时把该次调用实际使用的密钥做哈希，写进
`other.admin_info.channel_key_fp`。BFF 用同一算法把候选密钥算成指纹，直接按指纹反查。位置
怎么变都不影响。

**为什么不是给密钥建 id**：`channels.key` 是一列文本，多把密钥只是同一条字符串里的不同片段，
没有主键；`GetKeys()` 有 17 处调用，`MultiKeyStatusList` 等状态 map 全以序号为键。引入独立
id 要动存储模型与上游前端，每次 merge 都要付冲突成本。哈希是纯增量：多写一个字段，两处读它，
零 schema 变更。

## 目标

- 每条直接调用产生的日志都带稳定的密钥指纹。
- BFF 按密钥明文精确反查日志，不受密钥重排影响。
- 不改变现有 `multi_key_index` 的写入与展示。

## 非目标

- 不给密钥建独立表或 id，不改 `channels` 存储模型。
- 不回填历史日志。产品已拍板：升级前的日志不再支持按密钥明文反查。
- 不改前端界面。列表仍按 `multi_key_index` 显示「Key N」。
- **JSON 数组形式的渠道密钥不纳入**（典型是 Vertex service account）。见「范围外」。
- 任务型渠道的退款/差额结算日志不带指纹。见「范围外」。

## 已定决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 历史日志兼容 | 只认指纹，老日志放弃 | 产品拍板。BFF 只留一条查询路径。 |
| 写入范围 | 所有渠道都写 | 单密钥渠道换钥匙后也能区分；成本是一次 sha256。 |
| 取哪个 key | **`ContextKeyChannelKey`**（日志写入时刻的值） | 见下节。 |
| 存储位置 | `other.admin_info.channel_key_fp` | `admin_info` 由 `formatLogOtherJSON` 对普通用户整体剥离（`model/log_other.go:246-258`），天然只对管理员可见，与 `multi_key_index` 同作用域。 |
| JSON 数组 key | 不纳入 | BFF 侧要逐字节对齐数组元素的原始文本，得新写一个原文扫描器；这类渠道的 key 是凭证 JSON，不是运维会拿来搜的形态。 |
| 任务结算/退款日志 | 接受漏 | 这些路径只有 `context.Context`，没有 `gin.Context`；要覆盖就得把指纹持久化进 `Task.PrivateData`，成本与收益不成比例。 |

## 关键事实（已核对代码）

### 为什么取 `ContextKeyChannelKey` 而不是 `GetNextEnabledKey` 的返回值

初版设计打算在 `middleware/distributor.go:714` 拿到 `(key, index)` 时就把指纹算好放进上下
文。核对后这个方向是错的：

插件类渠道会在选定之后把 `ContextKeyChannelKey` 改写成刷新后的凭据行
（`relay/channel/providerplugin/adaptor.go:576`、`relay/channel/task/jsplugin/credential.go:69`），
而刷新结果**会回写 `channels.key`**
（`service/provider_plugin.go:250-252` 的 `lines[index] = refreshed; channel.Key = strings.Join(...)`，
经 `model/channel_plugin_credential.go:64-71` 写入 `key` 列）。

所以日志写入时刻的 `ContextKeyChannelKey` 恰好等于上游实际发出去的字节，也等于库里当前存的
那一行——正是管理员能粘贴的那份明文。而 `GetNextEnabledKey` 返回的是刷新**之前**的旧值，
拿它算指纹恰恰会永久搜不到。

连带收益：**不需要新增 context key，不需要改 `distributor.go`，不需要改 WS 重连的
`lockedContext` 白名单，也不需要改 `relay/relay_task.go`**——`lockedContext` 本来就含
`ContextKeyChannelKey`（`relay/responses_websocket.go:387`），换渠道分支也已经在设置它
（`relay/relay_task.go:109`）。上游改动因此收敛为「一个 helper 文件 + 一处写日志」。

## 指纹算法

```
fingerprint(key) = hex(sha256(key))[:16]
```

- **不做任何规范化**，连 trim 都不做。理由见下节。
- `hex` 小写。
- 16 位十六进制 = 64 bit。
- 不加盐：`admin_info` 只对管理员可见，真实密钥是高熵随机串，离线爆破不可行。这一点写进
  代码注释，避免后人以为漏了盐。
- 不复用 `service/affinityFingerprint`（`service/channel_affinity.go:415`）：它用 sha1 取 8 位，
  只作缓存键；这里要当查询键，8 位在密钥上千后碰撞不可忽略，sha1 也没有理由再用。

### 为什么连 trim 都不做

初版写的是「`TrimSpace` / `trim()` 后哈希」。实测两侧的空白字符集**不一致**：

| 前缀字符 | Go `strings.TrimSpace` | JS `.trim()` |
|---|---|---|
| U+FEFF BOM | 保留 | 剥掉 |
| U+0085 NEL | 剥掉 | 保留 |
| U+00A0 NBSP | 剥掉 | 剥掉 |
| U+2028 LS | 剥掉 | 剥掉 |

渠道密钥从文件复制时带 BOM 并不罕见，这会让两侧算出的指纹永远对不上，表现是「按密钥搜不
到」。

而 trim 本身是多余的：两侧的哈希输入**本来就是同一个字符串**。上游取
`ContextKeyChannelKey`，它的值是 `GetNextEnabledKey` 返回的元素（多密钥来自
`strings.Split(strings.Trim(channel.Key,"\n"),"\n")`，单密钥是 `channel.Key` 原样）；BFF 拆出
的也是同一个元素（`key.replace(/^\n+|\n+$/g,'').split('\n')`，单密钥 `[r.key]` 原样）。用户
粘贴的明文只用于判定命中，不参与哈希。

输入既已逐字节相同，规范化就是净增风险。去掉它，两侧零可能漂移。

### 两侧一致性只需要一条规则

**指纹永远基于「存储态原文」计算**，不基于用户粘贴的内容：

- 上游：对 `ContextKeyChannelKey` 算，即库里那一行的字节。
- BFF：从 `channels.key` 拆出候选元素，对元素算。用户粘贴的明文**只用于判定哪把 key 命中**
  （沿用现有的去空白比较），不参与哈希。

因此两侧只需就「元素边界」一致，不需要就「用户输入如何规范化」达成一致。边界规则复用 BFF
现有的 `splitChannelKeys`：

- 非多密钥渠道：整条 `channel.Key` 就是一个候选（对齐上游
  `GetNextEnabledKey` 非多密钥分支直接返回 `channel.Key`）。
- 多密钥渠道（换行分隔）：按 `splitChannelKeys` 拆出的元素各算一个候选。
- 多密钥渠道且 key 是 JSON 数组：**不产生候选**。这类渠道的日志会有指纹，但 BFF 永远匹配
  不上——即「范围外」的既定行为，不额外写检测代码。
- 多密钥渠道、`[` 开头但 JSON 非法：上游 `GetKeys()` 会回落到换行口径，BFF 必须一样。

### 展示侧也用指纹

`upstreamKeyOf` 目前只按 `multi_key_index` 取那一把。多密钥渠道却缺序号的日志（渠道由单密钥
改成多密钥后，改造前写入的行）会把**整个密钥池**当成那一把下发——掩码是拼接串的掩码，
`/reveal` 直接给出多行文本。

改为优先用指纹回填：

```
1. 渠道无 key → null
2. 有指纹，且 sha256(整条 key) == 指纹 → 整条 key（单密钥渠道）
3. 有指纹，在换行拆出的行里匹配 → 那一把（多密钥渠道）
4. 无指纹或都未命中 → 保持原有逻辑（keyIndex === null 返回整条 key，否则按序号取）
```

第 2 步先于第 3 步，保证单密钥渠道里带换行的凭证（Vertex service account）不被拆坏。
JSON 数组渠道第 2、3 步都不命中，落回第 4 步。

**第 4 步必须与改动前逐字一致。** 单密钥渠道的日志本来就没有 `multi_key_index`，若把它改
成返回 `null`，所有老日志会从显示掩码变成 `—`——这是回归，不是「不管老日志」。
「不管老日志」指的是不回填指纹，不是改变它们的现有展示。

已实测的八种组合：新增的多密钥缺序号日志取到正确那一把；序列号与指纹冲突时以指纹为准；
单密钥渠道含换行的 key 不被截断；老日志三条路径与改动前完全一致。


## 范围外（明确接受的缺口）

| 缺口 | 现象 | 原因 |
|---|---|---|
| 升级前的日志 | 按密钥明文搜不到 | 没有指纹字段，且不回填。展示侧也不回填，但仍走原有回落逻辑，显示不受影响 |
| 升级前写入的多密钥渠道缺序号日志 | 掩码与 reveal 给出拼接串 | 没有指纹可回填；升级后新写入的日志已靠指纹修正 |
| JSON 数组形式的渠道密钥（Vertex 等多密钥） | 该渠道日志按密钥搜不到 | 上游存的是 `json.RawMessage` 原文，BFF 现有 `splitChannelKeys` 会 `JSON.stringify` 重排；逐字节对齐需要新写原文扫描器 |
| 任务型渠道的退款/差额结算日志（`model.RecordTaskBillingLog`） | 搜不到 | `RefundTaskQuota` / `RecalculateTaskQuota` 只有 `context.Context`（`service/task_billing.go:267`、`:314`），拿不到 gin 上下文 |
| Midjourney 退款日志（`service/midjourney.go:122`） | 搜不到 | 同上，无上下文 |

## 上游改动（new-api）

### 1. 新增 helper

`service/channel_key_fingerprint_fork.go`：

```go
// ChannelKeyFingerprint returns the stable identifier a log records for the
// upstream key a request used. The BFF stores the same value to search logs by
// key secret, so both sides must hash the stored key text and nothing else.
//
// No normalization at all: the bytes hashed here are the same bytes the BFF
// hashes, so any trim would only add a way for the two sides to disagree.
// Go's TrimSpace and JS's trim() do not cover the same characters (U+FEFF is
// whitespace to JS but not to Go, U+0085 the other way round).
//
// The value is unsalted on purpose: it is only ever written under
// other.admin_info (stripped for non-admin viewers), and a real upstream key
// has enough entropy that an offline attack on a 64-bit prefix is pointless.
func ChannelKeyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	return hex.EncodeToString(common.Sha256Raw([]byte(key)))[:16]
}
```

`common.Sha256Raw` 已存在（`common/hash.go:10`），无需新依赖。

### 2. 写进日志

`service/log_info_generate.go` 的 `AppendRelayLogAdminInfo`，紧接现有
`is_multi_key` / `multi_key_index` 块之后（加 `// [FORK]` 标记）：

```go
if fp := ChannelKeyFingerprint(common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)); fp != "" {
	other.SetAdmin("channel_key_fp", fp)
}
```

空串不写，与 `multi_key_index` 的「无值即缺字段」语义一致。此函数已覆盖文本/音频/WSS 消费
日志（`GenerateTextOtherInfo`）、错误日志（`service/relay_error.go:97`）、Midjourney 提交与
轮询（`relay/mjproxy_handler.go:286`、`:665`）。

### 3. 违规费日志

`service/violation_fee.go:137-150` 手工构建 `LogOther` 后直接 `RecordConsumeLog`，不走
`AppendRelayLogAdminInfo`。它有 `gin.Context`，补一行同一段逻辑：

```go
if fp := service.ChannelKeyFingerprint(common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)); fp != "" {
	other.SetAdmin("channel_key_fp", fp)
}
```

（该文件在 `package service` 内，直接调 `ChannelKeyFingerprint`。）

**注意**：若这段逻辑要在两处出现，就抽成一个导出的
`AppendChannelKeyFingerprint(ctx *gin.Context, other *model.LogOther)`，由
`AppendRelayLogAdminInfo` 与 `violation_fee.go` 各调一次。两处调用已满足
`AGENTS.md`「单一调用者的 helper 应内联」的反面条件，抽函数是合适的。

### 4. 文件清单

| 文件 | 改动 | 类型 |
|---|---|---|
| `service/channel_key_fingerprint_fork.go` | 规范化 + 哈希 + 写 admin_info | 新增 |
| `service/channel_key_fingerprint_fork_test.go` | 向量与路径测试 | 新增 |
| `service/log_info_generate.go` | +1 处调用 | 改上游 |
| `service/violation_fee.go` | +1 处调用 | 改上游 |
| `fork/README.md` | 定制登记册加一节 | 改 |

上游其余文件零改动：无 `constant/`、无 `middleware/`、无 `relay/`、无 schema 迁移。

## BFF 改动（token-master）

### 解析：从「渠道 + 序号」改为「渠道 + 候选指纹集」

`bff/src/modules/logs/logs.service.ts`：

- `UpstreamKeyPair` 改为 `{ channelId: number; fingerprints: string[] }`。
- `resolveUpstreamKey`：
  - 粗筛 SQL 不变（`strpos(regexp_replace(c.key,'\s+','','g'), $needle) > 0`）。
  - 非多密钥渠道：`stripWhitespace(r.key) === needle` 时候选 = 整条 key 的指纹（1 个）。
  - 多密钥渠道：元素 `stripWhitespace(part) === needle` 的，候选 = 该元素的指纹。
  - 只保留候选集非空的渠道。
- 新增 `channelKeyFingerprint(key: string): string`——`createHash('sha256').update(key).digest('hex').slice(0, 16)`，**同样不 trim**。
- 新增常量 `CHANNEL_KEY_FP_TEXT = \`${OTHER_JSONB} -> 'admin_info' ->> 'channel_key_fp'\``。
- `upstreamKeyClause` 改为 `(channel_id = $c AND ${CHANNEL_KEY_FP_TEXT} = ANY($fps::text[]))`，
  **删掉** `keyIndex === null` 的回落分支与 `multi_key_index` 条件。

### 展示：用指纹回填那两把密钥

`upstreamKeyOf` 改为优先用日志里的指纹定位，`multi_key_index` 只作老日志的回落：

- `ROWS_SQL` 与 `RevealRawRow` 各加一列 `${CHANNEL_KEY_FP_TEXT} AS channel_key_fp`（文本，
  不走 `safeInt`）。
- `AdminLogRow.upstream_key_index` 的语义不变，仍从 `multi_key_index` 读，供界面显示「Key N」。
- `upstreamKeyOf(channel, keyIndex, fingerprint)`：按上节「展示侧也用指纹」的四步。
- 列表掩码与 `/reveal` 共用同一个 `upstreamKeyOf`，一处改完两处生效。

**单密钥渠道的搜索语义（有意）**：以前只按 `channel_id` 筛，渠道换过钥匙后仍会命中所有历史
日志；现在要求指纹相等。这是「精确匹配」的含义，产品已确认。

### 密钥明文仍不进 SQL 参数

现有测试「上游秘钥明文不进入任何 SQL 参数」继续有效：新增的 `$fps` 是哈希，不是明文。

## 发布顺序（必须遵守）

1. 先发新版 new-api 镜像（开始写 `channel_key_fp`）。
2. 再发 BFF（开始按指纹搜索）。

反过来的后果是**静默空结果**：BFF 按 `channel_key_fp` 筛，库里一条都没有，界面表现为「查无
此钥」，看起来像 bug 而不像功能未就绪。两版之间可以有任意长的观察期，期间上游只多写一个字
段，BFF 仍按旧逻辑工作。

无数据库迁移：`logs.other` 是 text 列，加字段不需要 schema 变更，BFF 用 `OTHER_JSONB` 读。

## 验证

**Go**（`/Users/kangkang/Documents/hkzy/new-api`）

```bash
go test ./service/... ./middleware/... ./relay/...
gofmt -l service/channel_key_fingerprint_fork.go
go vet ./service/...
```

必须覆盖：

1. `ChannelKeyFingerprint` 向量：普通串、**首尾空白原样参与哈希**（不 trim）、`\r\n` 行尾、
   单密钥多行 JSON（内部换行保留）、**BOM / NEL 前缀与 JS 侧算出同一个值**、空串返回空。
2. `AppendRelayLogAdminInfo`：上下文有 `ContextKeyChannelKey` → `admin_info.channel_key_fp` 存在且
   等于 `ChannelKeyFingerprint(key)`；上下文无 key → 字段缺失。
3. 插件渠道：上下文里是被改写后的刷新行时，写入的指纹对应刷新行（即落库值），用例直接
   `SetContextKey(ContextKeyChannelKey, refreshed)` 后断言。
4. WS 重连：`lockedContext[ContextKeyChannelKey]` 还原后，日志仍写得出同一指纹。

**BFF**（`token-master/.claude/worktrees/pr-bff/bff`）

```bash
pnpm test && pnpm typecheck
```

必须覆盖：

1. SQL 出现 `channel_key_fp`，不再把 `multi_key_index` 当筛选条件。
2. 参数里不含密钥明文（沿用现有用例，改断言）。
3. **与 Go 共用同一组向量**：把同样的 `(input, expected fp)` 表格抄进
   `logs.service.spec.ts` 与 Go 的测试文件，注释互相指向对方路径。这是整个方案唯一真正脆
   弱的地方——算法一漂移，表现是「按密钥搜不到」，看起来像 bug 而不像配置问题。
4. 单密钥渠道：候选 1 个，条件为 `channel_id + fp`。
5. 多密钥渠道：命中的元素产生 1 个候选，未命中的元素不产生。
6. 展示回填：多密钥渠道 + 日志有序号 → 取那一把；**有序号但指纹指向另一把**（密钥重排后）
   → 取指纹那一把；多密钥渠道 + 缺序号 + 有指纹 → 取指纹那一把（不再返回拼接串）。
7. 单密钥渠道 key 含换行 + 有指纹 → 返回整条，不截断成第一行。

## 风险

| 风险 | 影响 | 处理 |
|---|---|---|
| 两侧哈希输入漂移 | 搜索永远无结果，且看起来像「查无此钥」 | 不 trim，输入本就同源；共用测试向量，含 BOM/NEL 用例 |
| 先发 BFF | 静默空结果 | 发布顺序写进本节与 worklog |
| 插件渠道取到刷新前的 key | 该渠道检索全空 | 取 `ContextKeyChannelKey`；用例 3 专门锁住 |
| 多密钥渠道缺序号的老日志 | 掩码与 reveal 给出拼接串 | 新日志靠指纹回填已修好；老日志不回填，属已接受缺口 |
| `docs/页面能力盘点.md` 的旧口径 | 文档说「按 (channel_id, 序号) 下推」 | 改 BFF 时同步更新该段 |
