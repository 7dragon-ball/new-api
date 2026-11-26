package service

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	upstreamURLs := common.UpstreamAddresses(c)
	logger.LogError(c, common.LogText("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	RecordRelayErrorLog(c, channelError.ChannelId, err, relayInfo)
}

// RecordRelayErrorLog writes the error log of a failed relay request when
// error logs are on. channelId is 0 for a request that reached no channel.
func RecordRelayErrorLog(c *gin.Context, channelId int, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return
	}
	userId := c.GetInt("id")
	tokenName := c.GetString("token_name")
	modelName := c.GetString("original_model")
	tokenId := c.GetInt("token_id")
	userGroup := c.GetString("group")
	other := model.NewLogOther()
	if c.Request != nil && c.Request.URL != nil {
		other.SetPublic("request_path", c.Request.URL.Path)
	}
	other.SetPublic("error_type", err.GetErrorType())
	other.SetPublic("error_code", err.GetErrorCode())
	// [FORK] 使用日志记录调用状态码；归一到二开码后另存上游原始码，避免丢失 provider 原因。
	// 二开：401/429 会按状态码把 error_code 归一到 upstream_* 二开码；这里额外保留上游自带
	// 的错误码，避免丢失 provider 的具体原因（如 invalid_api_key / insufficient_quota）。
	// GetUpstreamErrorCode 仅在错误来自上游响应体时非空，new-api 内部哨兵不会混入。
	if upstreamCode := err.GetUpstreamErrorCode(); upstreamCode != "" && upstreamCode != err.GetErrorCode() {
		other.SetAdmin("upstream_error_code", upstreamCode)
	}
	other.SetPublic("status_code", err.StatusCode)
	AppendRelayLogAdminInfo(c, relayInfo, other)
	appendRequestPluginRecord(relayInfo, other)
	AppendResponseModelLogInfo(relayInfo, other)
	AppendTaskPluginContextAuditInfo(c, other)
	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	// [FORK] 使用日志 content 必须与读者语言无关：错误对象可携带落库专用固定英文文案
	// （如定价失败的文案是按读者语言渲染的），有则用它作为 content；只换落库副本，
	// 原错误对象不变，响应仍是读者语言。
	contentErr := err
	if fixedMessage := err.GetLogMessage(); fixedMessage != "" {
		snapshot := *err
		snapshot.Err = errors.New(fixedMessage)
		contentErr = &snapshot
	}
	model.RecordErrorLog(c, userId, channelId, modelName, tokenName, contentErr.MaskSensitiveErrorWithStatusCode(common.UpstreamAddresses(c)...), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
}

// [FORK] 预扣费/额度不足这类错误带抑制标记会被跳过落库，强制落库让使用日志能看见该类失败态。
// ForceRecordRelayErrorLog 落库即使错误对象显式带了 types.ErrOptionWithNoRecordErrorLog。
// 二开：预扣费/额度不足这类错误会带该抑制标记，被 RecordRelayErrorLog 主动跳过，导致使用
// 日志看不到这类失败态；这里清除抑制标记后落库，保留其
// error_type/RelayError/error_code/status_code 等全部信息（仍受 constant.ErrorLogEnabled 开关控制）。
//
// 落库 content 必须是固定英文：额度类失败的文案由 billing 层用 i18n.T(c, ...) 按读者语言生成，
// 直接落库会让同一种失败在不同语言请求下写出不同 content，使用日志无法检索与聚合（中间件路径
// 已用 abortWithI18nMessage 消除同类问题）。这里只换掉交给落库的副本的文案，原错误对象不变，
// 响应仍是读者语言。定价类失败（ErrorCodeModelPriceError）的文案在生成处就自带了固定英文
// （types.ErrOptionWithLogMessage），由 RecordRelayErrorLog 统一替换，不在本函数的 switch 内。
func ForceRecordRelayErrorLog(c *gin.Context, channelId int, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	err.SetRecordErrorLog(true)
	fixedMessage := ""
	switch err.GetErrorCode() {
	case types.ErrorCodeInsufficientUserQuota:
		fixedMessage = "Insufficient user quota"
	case types.ErrorCodePreConsumeTokenQuotaFailed:
		fixedMessage = "Not enough quota to pre-consume for this request"
	}
	logErr := err
	if fixedMessage != "" {
		logOnly := *err
		logOnly.Err = errors.New(fixedMessage)
		logErr = &logOnly
	}
	RecordRelayErrorLog(c, channelId, logErr, relayInfo)
}
