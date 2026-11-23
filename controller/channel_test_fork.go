package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const redactedCredential = "[REDACTED]"

// [FORK] POST /api/channel/test —— 按凭据测试渠道连通性（新建渠道尚无 id 时用）。
//
// 与 GET /api/channel/test/:id 跑的是同一个 testChannel()，区别只在渠道对象来自请求体
// 而不是数据库：Id 保持 0，testChannel 会据此跳过定价校验与结算/消费日志。
// body: { type, base_url?, key, model, endpoint_type?, stream? }；响应形状与 TestChannel 完全一致。
func TestChannelByCredentials(c *gin.Context) {
	var req struct {
		Type         int    `json:"type"`
		BaseURL      string `json:"base_url"`
		Key          string `json:"key"`
		Model        string `json:"model"`
		EndpointType string `json:"endpoint_type"`
		Stream       bool   `json:"stream"`
	}
	if err := common.DecodeJsonBody(c.Request.Body, &req); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}

	// 多行秘钥只取第一把，与 POST /api/channel/fetch_models 同口径：
	// 先整体 trim 再切（前导空行很常见），Codex 的凭据本身是多行 JSON 要原样保留。
	// key 允许为空：「自建算力」走内网网关，上游不校验秘钥（保存路径也不检查，
	// 见 validateChannel 的 isAdd=false 分支）。
	key := credentialKeyFor(req.Type, req.Key)
	secrets := credentialSecrets(req.Type, key)
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
	// 注意两点：
	//   - 它**不校验 base_url 格式**（`controller/channel.go:580-700`），非法地址会在
	//     testChannel 的 adaptor 阶段或 DoRequest 处失败，最终以 success:false 回给前端。
	//   - 它要求依赖渠道 settings/Other 的类型带上对应配置，而本接口只收
	//     type/base_url/key/model，所以 TaskPlugin（缺 task_plugin_key）与 VertexAI
	//     （缺部署区域）等类型会在这里返回 400，测不到连通性。save 之后走
	//     `GET /api/channel/test/:id` 能测，新建弹窗不能。
	if err := validateChannel(channel, false); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, redactCredentialError(err, secrets))
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
	result := testChannel(requestCtx, channel, testUserID, modelName, strings.TrimSpace(req.EndpointType), req.Stream)
	consumedTime := time.Since(tik).Seconds()

	if result.localErr != nil {
		resp := gin.H{
			"success": false,
			// 上游错误体可能回显它收到的 Authorization；redactCredentialError 会把
			// 凭据原文及其 URL 编码形式替换成 [REDACTED]。
			"message": redactCredentialError(result.localErr, secrets).Error(),
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
			"message":    redactCredentialError(result.newAPIError, secrets).Error(),
			"time":       consumedTime,
			"error_code": result.newAPIError.GetErrorCode(),
		})
		return
	}
	// 预览态没有渠道可归属，不更新 response_time（TestChannel 在这里有 go channel.UpdateResponseTime）。
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "time": consumedTime})
}

// credentialKeyFor 从用户提交的秘钥串里取出这次要用的一把，
// 口径与 POST /api/channel/fetch_models（controller/channel.go:1717）一致：
// 先整体 trim，Codex 保留完整多行 JSON，其余类型取第一行。
// 两边共用本函数，不要再各自内联一份。
func credentialKeyFor(channelType int, raw string) string {
	key := strings.TrimSpace(raw)
	if channelType != constant.ChannelTypeCodex {
		key = strings.Split(key, "\n")[0]
	}
	return key
}

// credentialSecrets 返回本次渠道测试要脱敏的凭据串：秘钥本身，Codex 再加 OAuth 的
// access_token / id_token / refresh_token。
//
// Codex 发给上游的 Authorization 只是 access_token（relay/channel/codex），它是整段
// JSON 秘钥的子串，只替换秘钥原文匹配不到。
func credentialSecrets(channelType int, key string) []string {
	secrets := []string{key}
	if channelType != constant.ChannelTypeCodex {
		return secrets
	}
	var oauthKey struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := common.Unmarshal([]byte(strings.TrimSpace(key)), &oauthKey); err != nil {
		return secrets
	}
	return append(secrets, oauthKey.AccessToken, oauthKey.IDToken, oauthKey.RefreshToken)
}

// redactResponseBody 把上游响应体里的凭据替换掉再回填。
//
// RelayErrorHandler 在「body 是 JSON 但取不到 error message」时会直接把原始 body 写进
// 日志，在它外面事后脱敏来不及，所以得先替换再交给它。读失败也不 fail-open：
// 拿到的那段 raw 同样可能是含凭据的原文。
func redactResponseBody(body io.ReadCloser, secrets []string) io.ReadCloser {
	raw, _ := io.ReadAll(body)
	_ = body.Close()
	return io.NopCloser(strings.NewReader(redactCredentialText(string(raw), secrets)))
}

// redactCredentialText 替换文本里的凭据：原文、URL 编码形式（上游回显查询串时是编码
// 后的），与响应 message 那条路径同一口径。
func redactCredentialText(text string, secrets []string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			text = strings.ReplaceAll(text, form, redactedCredential)
		}
	}
	return text
}

// redactCredentialError 脱敏错误文本；没有替换到任何东西时原样返回，保留
// *common.Message 的 message_key（前端靠它选语言包），真替换过才丢类型。
func redactCredentialError(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	// url.Error 带着完整请求 URL（查询串里可能有 key），不回给前端。
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	text := err.Error()
	redacted := redactCredentialText(text, secrets)
	if redacted == text {
		return err
	}
	return errors.New(redacted)
}
