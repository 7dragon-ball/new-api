package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	userId := c.GetInt("id")
	_, preparedPluginRoute := c.Get(pluginruntime.ContextKeyRouteRequest)
	if !preparedPluginRoute || !RespondTaskPluginError(c, &dto.TaskError{
		Code:       codeStr,
		Message:    message,
		StatusCode: statusCode,
	}) {
		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
				"type":    "new_api_error",
				"code":    codeStr,
			},
		})
	}
	c.Abort()
	logger.LogError(c.Request.Context(), common.LogText("user %d | %s", userId, message))
	// 二开：前置校验/鉴权失败（模型未上架、无可用渠道、无模型权限、密钥不可用等）此前
	// 只回响应不落库，使用日志看不到失败态，故补记一条错误日志。此时尚无 relayInfo，
	// 传 nil（RecordRelayErrorLog 的各元信息填充已对 nil 做保护）。
	// 落库只覆盖「已认证用户的、可归类的客户端失败」：
	//   - 未认证（userId == 0，如无效密钥、扫描流量）没有归属用户，写库只会造成日志放大；
	//   - 本地模型限流 429 高频且无诊断价值（middleware/model-rate-limit.go）；
	//   - 5xx 服务端故障推导不出用户可见的调用状态，交由上面的 logger.LogError 记录。
	if userId == 0 || statusCode == http.StatusTooManyRequests {
		return
	}
	// 调用方未指定错误码时按状态码推导，避免使用日志里 error_code 为空。
	logCode := codeStr
	if logCode == "" {
		logCode = string(defaultErrorCodeForStatus(statusCode))
	}
	if logCode == "" {
		return
	}
	// 先记录终止事件再落库：RecordRelayErrorLog 会快照 request_policy，若等到 Distribute
	// 的 defer 才追加终止事件，落库内容就会缺少这次失败对应的决策。
	logErr := types.NewErrorWithStatusCode(errors.New(message), types.ErrorCode(logCode), statusCode)
	service.RecordRequestPolicyTermination(c, logErr)
	service.RecordRelayErrorLog(c, 0, logErr, nil)
}

// defaultErrorCodeForStatus maps a client-failure HTTP status to the error code
// recorded when the caller supplied none, so usage logs carry a meaningful status
// instead of an empty error_code. Only 4xx statuses have a user-visible code;
// callers skip recording when this returns "".
func defaultErrorCodeForStatus(statusCode int) types.ErrorCode {
	switch statusCode {
	case http.StatusUnauthorized:
		return types.ErrorCodeInvalidToken
	case http.StatusForbidden:
		return types.ErrorCodeAccessDenied
	}
	if statusCode >= 400 && statusCode < 500 {
		return types.ErrorCodeInvalidRequest
	}
	return ""
}

// invalidRequestStatus is the status of a request whose body could not be
// read or parsed: 413 when the body exceeds MAX_REQUEST_BODY_MB, else 400.
func invalidRequestStatus(err error) int {
	if common.IsRequestBodyTooLargeError(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}
