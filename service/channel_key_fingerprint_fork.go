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

// AppendChannelKeyFingerprint 把日志写入时刻实际使用的密钥指纹记进 admin_info。
//
// 取 ContextKeyChannelKey 而不是 GetNextEnabledKey 的返回值：插件类渠道会在选定后
// 把该键改写成刷新后的凭据行，且刷新结果会回写 channels.key
// （service/provider_plugin.go 的 UpdateChannelLocked 分支）。所以上下文里的值才是
// 上游真正发出去、也是管理员能从库里读到的那一份明文；取选择时的旧值会永久搜不到。
//
// 该键在 middleware/distributor.go 里无条件设置（不在 IsMultiKey 分支内），因此单密钥
// 渠道与错误日志同样带得上指纹。
func AppendChannelKeyFingerprint(ctx *gin.Context, other *model.LogOther) {
	if ctx == nil || other == nil {
		return
	}
	if fp := ChannelKeyFingerprint(common.GetContextKeyString(ctx, constant.ContextKeyChannelKey)); fp != "" {
		other.SetAdmin("channel_key_fp", fp)
	}
}
