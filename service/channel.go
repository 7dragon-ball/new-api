package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

func shouldCloseActiveWebSocketsAfterDisable(channelId int) (bool, *model.Channel) {
	channel, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.SysLog(common.LogText("failed to check channel status before closing active websockets: channel_id=%d, error=%v", channelId, err))
		return true, nil
	}
	return channel.Status != common.ChannelStatusEnabled, channel
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	common.SysLog(common.LogText("channel %q (#%d) failed, disabling it, reason: %s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(common.LogText("channel %q (#%d) has automatic disabling turned off, skipping", channelError.ChannelName, channelError.ChannelId))
		return
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
		closeWebSockets, channel := shouldCloseActiveWebSocketsAfterDisable(channelError.ChannelId)
		if closeWebSockets {
			CloseActiveWebSocketsForChannel(channelError.ChannelId, ChannelDisabledCloseReason)
		}
		params := map[string]any{"Name": channelError.ChannelName, "Id": channelError.ChannelId, "Reason": reason}
		NotifyChannelWatchers(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), func(lang string) (string, string) {
			return i18n.Translate(lang, i18n.MsgChannelNotifyDisabledSubject, params), i18n.Translate(lang, i18n.MsgChannelNotifyDisabledContent, params)
		})
		if common.IsMasterNode && channel != nil {
			scheduledPlugins.disabled(channel, channelError.UsingKey, time.Now())
		}
	}
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		params := map[string]any{"Name": channelName, "Id": channelId}
		NotifyChannelWatchers(formatNotifyType(channelId, common.ChannelStatusEnabled), func(lang string) (string, string) {
			return i18n.Translate(lang, i18n.MsgChannelNotifyEnabledSubject, params), i18n.Translate(lang, i18n.MsgChannelNotifyEnabledContent, params)
		})
	}
}

// [FORK] 自动停用的 channel:* 白名单。
//
// 上游对 `IsChannelError` 一律停用，但这一个分支里混了两类性质完全不同的错误：
//   - 秘钥问题（`channel:invalid_key` / `channel:no_available_key`）—— 换钥或补钥即可恢复；
//   - 渠道配置错误（参数覆盖表达式 / header 覆盖 / 模型映射 / AWS 适配器 / 测试超时）——
//     停用后不会自愈，且与秘钥是否失效无关，必须由人改配置。
//
// 产品口径是「只有秘钥类原因可以自动停用」，故只放行秘钥两类。
// 配置类错误**直接判定为不停用**，不会再走后面的状态码与关键词判据 —— 否则
// `param_override` 的 return_error 只要把 status_code 写成 401、或文案里带上额度类
// 词汇，就会绕过白名单重新被停用。
// 回归测试：service/channel_disable_fork_test.go。
func isKeyRelatedChannelError(err *types.NewAPIError) bool {
	switch err.GetErrorCode() {
	case types.ErrorCodeChannelInvalidKey, types.ErrorCodeChannelNoAvailableKey:
		return true
	}
	return false
}

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	// [FORK] channel:* 分支直接给出结论，不再往下走：
	// 秘钥两类 → 停用；配置类 → 不停用。
	// 不能让配置类落到后面的状态码 / 关键词判据：`param_override` 的 return_error 允许
	// 运营自定义 status_code（写成 401 就会命中停用码段），文案也可能恰好含额度关键词。
	if types.IsChannelError(err) {
		return isKeyRelatedChannelError(err)
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
