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
	// 429 在本项目中只来自本地模型限流（middleware/model-rate-limit.go），高频且无诊断
	// 价值，跳过落库以免使用日志被刷屏。
	if statusCode == http.StatusTooManyRequests {
		return
	}
	// 调用方未指定错误码时按状态码推导，避免使用日志里 error_code 为空。
	logCode := codeStr
	if logCode == "" {
		logCode = string(defaultErrorCodeForStatus(statusCode))
	}
	service.RecordRelayErrorLog(c, 0, types.NewErrorWithStatusCode(errors.New(message), types.ErrorCode(logCode), statusCode), nil)
}

// defaultErrorCodeForStatus maps an HTTP status to the error code recorded when
// the caller supplied none, so usage logs carry a meaningful status instead of an
// empty error_code. Non-4xx statuses (server errors) keep an empty code.
func defaultErrorCodeForStatus(statusCode int) types.ErrorCode {
	switch statusCode {
	case http.StatusUnauthorized:
		return types.ErrorCodeInvalidToken
	case http.StatusForbidden:
		return types.ErrorCodeAccessDenied
	case http.StatusTooManyRequests:
		return types.ErrorCodeRateLimitExceeded
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
