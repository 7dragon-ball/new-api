package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// [FORK] POST /api/channel/test —— 按凭据测试渠道连通性（新建渠道尚无 id 时用）。
//
// 与 GET /api/channel/test/:id 跑的是同一个 testChannel()，区别只在渠道对象来自请求体
// 而不是数据库：Id 保持 0，testChannel 会据此跳过定价校验与结算/消费日志。
// body: { type, base_url?, key, model }；响应形状与 TestChannel 完全一致。
func TestChannelByCredentials(c *gin.Context) {
	var req struct {
		Type    int    `json:"type"`
		BaseURL string `json:"base_url"`
		Key     string `json:"key"`
		Model   string `json:"model"`
	}
	if err := common.DecodeJsonBody(c.Request.Body, &req); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}

	// 多行秘钥只取第一把，与 POST /api/channel/fetch_models 同口径。
	// key 允许为空：「自建算力」走内网网关，上游不校验秘钥（保存路径也不检查，
	// 见 validateChannel 的 isAdd=false 分支）。
	key := strings.TrimSpace(strings.Split(req.Key, "\n")[0])
	modelName := strings.TrimSpace(req.Model)
	if req.Type <= 0 || modelName == "" {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("type and model are required"))
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(req.Type)
	}
	channel := &model.Channel{Type: req.Type, Key: key, BaseURL: &baseURL}

	// 复用保存渠道时的前置校验（代理格式、HTTP transport 设置等）。
	// 注意它**不校验 base_url 格式**（`controller/channel.go:580-700`），非法地址会在
	// testChannel 的 adaptor 阶段或 DoRequest 处失败，最终以 success:false 回给前端。
	if err := validateChannel(channel, false); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, sanitizeFetchModelsError(err, key))
		return
	}

	testUserID, err := resolveChannelTestUserID(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	tik := time.Now()
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
	}
	result := testChannel(requestCtx, channel, testUserID, modelName, "", false)
	consumedTime := time.Since(tik).Seconds()

	if result.localErr != nil {
		resp := gin.H{
			"success": false,
			// 上游错误体可能回显它收到的 Authorization；sanitizeFetchModelsError 会把
			// 明文 key 及其 URL 编码形式替换成 [REDACTED]。
			"message": sanitizeFetchModelsError(result.localErr, key).Error(),
			"time":    0.0,
		}
		if result.newAPIError != nil {
			resp["error_code"] = result.newAPIError.GetErrorCode()
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	if result.newAPIError != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"message":    sanitizeFetchModelsError(result.newAPIError, key).Error(),
			"time":       consumedTime,
			"error_code": result.newAPIError.GetErrorCode(),
		})
		return
	}
	// 预览态没有渠道可归属，不更新 response_time（TestChannel 在这里有 go channel.UpdateResponseTime）。
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "time": consumedTime})
}
