package service

import (
	"encoding/hex"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// [FORK] 日志记录本次调用所用上游密钥的指纹，替代不稳定的 multi_key_index。
// 见 fork/design-channel-key-fingerprint.md。

// ChannelKeyFingerprint 是「这一行密钥是哪一把」的稳定标识。
//
// 输入必须是存储态原文（channels.key 里的那一行），不是用户粘贴的内容：BFF 侧也是
// 对库里拆出的候选算同一个值，两侧因此只需就「元素边界」一致，无需就「用户输入如何
// 规范化」达成一致。算法在 bff/src/modules/logs/logs.service.ts 的
// channelKeyFingerprint 里逐字复刻，改这里必须同时改那里。
//
// 不做任何规范化，连 TrimSpace 都不做：两侧的输入本来就是同一个字符串，而 Go 的
// TrimSpace 与 JS 的 trim() 覆盖的字符集不同（U+FEFF 对 JS 是空白、对 Go 不是；
// U+0085 反过来），加 strip 只会制造两侧对不上的可能。
//
// 不加盐是有意的：该值只写在 other.admin_info 下（对非管理员整体剥离），且真实上游
// 密钥是高熵随机串，对 64 bit 前缀做离线爆破没有意义。
func ChannelKeyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	return hex.EncodeToString(common.Sha256Raw([]byte(key)))[:16]
}

// 快照的短钥阈值：len(key) 低于此值时只落全星占位，不露任何原文片段。
// MaskTokenKey 在 9–13 位首尾重叠（9 位只藏 1 位），而这条串会进 admin_info，
// 对 role >= 10 可见并随 `/api/log/export` 批量导出，短钥接近可还原。
// 取 16 位：达到阈值时最多露 8 个字符（一半），40+ 位的真实上游钥约 1/5。
const channelKeyMaskedMinLen = 16

// channelKeyMaskedPlaceholder 是短钥的快照值。它不是 MaskTokenKey 的输出
// （后者对 ≥ channelKeyMaskedMinLen 的钥至少 18 字符），BFF 据此显示「已删除的短钥」。
const channelKeyMaskedPlaceholder = "***"

// AppendChannelKeyFingerprint 把日志写入时刻实际使用的密钥指纹与脱敏串记进 admin_info。
//
// 取 ContextKeyChannelKey 而不是 GetNextEnabledKey 的返回值：插件类渠道会在选定后
// 把该键改写成刷新后的凭据行（relay/channel/providerplugin/adaptor.go、
// relay/channel/task/jsplugin/credential.go），而刷新结果会经
// model.UpdateChannelLocked 的 updates["key"] 回写 channels.key
// （service/provider_plugin.go 改 channel.Key，model/channel_plugin_credential.go:71 落库）。
// 所以上下文里的值才是上游真正发出去、**正常情况下**也等于库里那一行的明文；
// 取选择时的旧值会永久搜不到。
//
// 存在一个窄竞态：刷新返回到写日志之间，另一节点可能再次刷新并落库，此时上下文
// 指纹对应的行已不在库里，BFF 从库里拆候选就算不出来，表现为偶发「按密钥搜不到」。
// 这是本方案的固有残余（替代方案是写日志时读库取值，更慢也更差），不额外处理。
//
// 该键在 middleware/distributor.go 里无条件设置（不在 IsMultiKey 分支内），因此单密钥
// 渠道与错误日志同样带得上指纹。
//
// [FORK] channel_key_masked 是同一把钥的脱敏串快照，解决「密钥被删除后历史日志失去归属」：
// 渠道密钥是硬删除（`channels` 表无 `deleted_at`，多把钥匙只是 `channels.key` 这一个
// text 列里的行），删掉后指纹在库里再也对不上任何候选，BFF 只能回落 `multi_key_index`，
// 而删除会让下标整体前移 —— 历史行会显示成**另一把仍在的密钥**，点「揭晓明文」还会把
// 那把钥的真身下发出去（张冠李戴比空着更糟）。
//
// 只存脱敏串、不存明文：`admin_info` 对 role >= 10 可见（model/log_other.go 的
// formatLogOtherJSON 只剥离 root_info），而 `GET /api/log/` 与 `/api/log/export`
// 都只挂 AdminAuth，落明文等于给运营开一条批量导出全站密钥的后门。脱敏串不可逆，
// 也答不了「把这把钥复制去排障」。
//
// 快照**只被展示**，不参与归属判定：判「已删除」只看字段是否存在，指纹命中时 BFF 用的是
// 候选自己算出的掩码。所以这条串的格式是本仓库内部的展示口径，不需要与 BFF 的
// maskTokenKey 逐字一致 —— 前 4 + 后 4 不唯一，一旦拿它当匹配键，就会以更粗的粒度复现
// 这里要修的张冠李戴。
//
// 短钥不落首尾片段：MaskTokenKey 在 9–13 位时首尾重叠（9 位只藏 1 位），而 admin_info 对
// role >= 10 可见、并随 `/api/log/export` 批量导出 —— 一条 9 位钥落进日志接近可还原。
// 因此 len(key) < channelKeyMaskedMinLen 只写全星占位，不写任何原文片段；展示上退化成
// 「已删除，且是短钥」。≥ 阈值的钥最多露 8 个字符（16 位时占一半，40+ 位的真实上游钥约
// 1/5），这条残余暴露是有意接受的已知风险，不是不可改的口径。
//
// 插件托管的 credential 行是整行 JSON，长度远超阈值，落的是整行的前 4 + 后 4。
func AppendChannelKeyFingerprint(ctx *gin.Context, other *model.LogOther) {
	if ctx == nil || other == nil {
		return
	}
	key := common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)
	if fp := ChannelKeyFingerprint(key); fp != "" {
		masked := channelKeyMaskedPlaceholder
		if len(key) >= channelKeyMaskedMinLen {
			masked = model.MaskTokenKey(key)
		}
		other.SetAdmin("channel_key_fp", fp)
		other.SetAdmin("channel_key_masked", masked)
	}
}
