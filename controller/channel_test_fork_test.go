package controller

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// previewTestEnv 准备一个最小可用的渠道测试环境：内存 sqlite + 一个 root 用户 + 已初始化的 i18n。
// i18n 必须在骗过定价校验失败分支之前初始化，否则 modelPriceNotConfiguredError 会 nil deref。
// 本文件与 channel_test_internal_test.go 共用。
func previewTestEnv(t *testing.T) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	require.NoError(t, db.Create(&model.User{
		Id: 7, Username: "preview-tester", Group: "default",
		Role: common.RoleRootUser, Status: common.UserStatusEnabled, Quota: 1_000_000,
	}).Error)
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
}

// chatOK 是一个只会成功回 chat completion 的假上游。
func chatOK(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
}

func callTestByCredentials(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	TestChannelByCredentials(ctx)
	var parsed map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &parsed))
	return recorder, parsed
}

func TestTestChannelByCredentialsSuccess(t *testing.T) {
	previewTestEnv(t)
	var hits int
	var seenPath, seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	body := `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-first\nsk-second","model":"gpt-4o-mini"}`
	recorder, parsed := callTestByCredentials(t, body)
	// Close 会等在途 handler 退出，之后读 handler 记下的值才没有竞态。
	upstream.Close()

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, true, parsed["success"])
	assert.Equal(t, 1, hits)
	assert.Equal(t, "/v1/chat/completions", seenPath)
	assert.Equal(t, "Bearer sk-first", seenAuth, "多行秘钥只取第一把")
}

func TestTestChannelByCredentialsRejectsBadInputBeforeUpstream(t *testing.T) {
	previewTestEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("入参非法时不得打上游")
	}))
	defer upstream.Close()

	for _, tc := range []struct{ name, body string }{
		{"空 model", `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-a","model":"  "}`},
		{"非法 type", `{"type":0,"base_url":"` + upstream.URL + `","key":"sk-a","model":"m"}`},
		{"body 非 JSON", `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, parsed := callTestByCredentials(t, tc.body)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Equal(t, false, parsed["success"])
		})
	}
}

// validateChannel 的错误是 common.NewMessage：前端靠 message_key 选语言包，脱敏不
// 能把没被替换过的错误拍平成普通 error（VertexAI 缺部署区域是可直接复现的一条）。
func TestTestChannelByCredentialsKeepsMessageKeyOnValidationError(t *testing.T) {
	previewTestEnv(t)

	body := `{"type":41,"base_url":"http://127.0.0.1:1","key":"sk-a","model":"m"}`
	recorder, parsed := callTestByCredentials(t, body)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "Deployment region cannot be empty", parsed["message_key"])
}

// 「自建算力」渠道的秘钥可以留空（内网网关不校验），预览测试不能把它挡在 400。
func TestTestChannelByCredentialsAllowsEmptyKey(t *testing.T) {
	previewTestEnv(t)
	var hits int
	upstream := chatOK(t, &hits)
	defer upstream.Close()

	body := `{"type":1,"base_url":"` + upstream.URL + `","key":"","model":"local-model"}`
	recorder, parsed := callTestByCredentials(t, body)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, true, parsed["success"], "空秘钥不应被 400 挡住：%v", parsed["message"])
	assert.Equal(t, 1, hits)
}

// handler 里最容易照抄 TestChannel 而漏掉的那一行是 `go channel.UpdateResponseTime(...)`：
// 预览态没有渠道可归属，它会让 gorm 报「WHERE conditions required」甚至误伤已有行。
func TestTestChannelByCredentialsDoesNotTouchChannelsTable(t *testing.T) {
	previewTestEnv(t)
	upstream := chatOK(t, nil)
	defer upstream.Close()

	require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Name: "keeper", Type: 1, Key: "k", Models: "m", Group: "default"}).Error)
	var before model.Channel
	require.NoError(t, model.DB.First(&before, 1).Error)

	body := `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-a","model":"gpt-4o-mini"}`
	recorder, parsed := callTestByCredentials(t, body)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, true, parsed["success"])

	var after model.Channel
	require.NoError(t, model.DB.First(&after, 1).Error)
	assert.Equal(t, before.TestTime, after.TestTime, "预览测试不得更新任何已存在渠道的 test_time")
	assert.Equal(t, before.ResponseTime, after.ResponseTime, "预览测试不得更新任何已存在渠道的 response_time")
}

// 上游 401 与「200 但 body 是 error」都必须变成 success:false + HTTP 200。
//
// 密钥泄漏这条要有效，假上游必须把 key 回显进错误体 —— 真实的 openai 兼容上游
// 通常在 message 里只写 "invalid api key"，那样断言永远成立、测不出任何东西。
func TestTestChannelByCredentialsUpstreamFailureIsNotHTTPError(t *testing.T) {
	previewTestEnv(t)
	for _, tc := range []struct {
		name  string
		code  int
		reply string
	}{
		// 故意把密钥拼进上游错误体，模拟「上游回显了它收到的 Authorization」
		{"401", http.StatusUnauthorized, `{"error":{"message":"invalid api key: sk-secret-value"}}`},
		{"200 带 error 体", http.StatusOK, `{"error":{"message":"model not found"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer upstream.Close()

			body := `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-secret-value","model":"m"}`
			recorder, parsed := callTestByCredentials(t, body)

			require.Equal(t, http.StatusOK, recorder.Code, "上游失败不能透成网关 4xx/5xx")
			assert.Equal(t, false, parsed["success"])
			assert.NotContains(t, recorder.Body.String(), "sk-secret-value", "响应里不得回显密钥")
		})
	}
}

// Codex 发给上游的 Authorization 只是秘钥 JSON 里的 access_token，它是整段秘钥的
// 子串：只替换秘钥原文匹配不到。上游回显 Authorization（或直接回显长期凭据）时，
// 响应 message 与 bad response 日志都不得出现 token。
func TestTestChannelByCredentialsRedactsCodexTokens(t *testing.T) {
	previewTestEnv(t)
	const (
		accessToken  = "at-secret-access-token"
		refreshToken = "rt-secret-refresh-token"
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad auth: ` + r.Header.Get("Authorization") + ` refresh: ` + refreshToken + `"}}`))
	}))
	defer upstream.Close()

	var logged bytes.Buffer
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logged
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	key, err := common.Marshal(map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"account_id":    "acct-1",
	})
	require.NoError(t, err)
	rows, err := common.Marshal(map[string]any{
		"type":     constant.ChannelTypeCodex,
		"base_url": upstream.URL,
		"key":      string(key),
		"model":    "gpt-5-codex",
	})
	require.NoError(t, err)

	recorder, parsed := callTestByCredentials(t, string(rows))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, false, parsed["success"])
	assert.Contains(t, recorder.Body.String(), "[REDACTED]")
	assert.NotContains(t, recorder.Body.String(), accessToken, "响应 message 不得回显 access_token")
	assert.NotContains(t, recorder.Body.String(), refreshToken, "响应 message 不得回显 refresh_token")
	assert.NotContains(t, logged.String(), accessToken, "日志不得出现 access_token")
	assert.NotContains(t, logged.String(), refreshToken, "日志不得出现 refresh_token")
}

// 前导空行很常见（textarea 里粘贴）。fetch_models 是先整体 trim 再切第一把，
// 新接口必须同口径，否则 "\nsk-real" 会被切成空串，静默以空 key 打上游。
func TestTestChannelByCredentialsTrimsBeforeSplittingKey(t *testing.T) {
	previewTestEnv(t)
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	body := `{"type":1,"base_url":"` + upstream.URL + `","key":"\nsk-real-key\nsk-second","model":"m"}`
	recorder, parsed := callTestByCredentials(t, body)
	upstream.Close()

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, true, parsed["success"], "%v", parsed["message"])
	assert.Equal(t, "Bearer sk-real-key", seenAuth, "前导空行不应把 key 切成空串")
}

// Codex 的凭据是多行 JSON，切开就废了。fetch_models 对 Codex 有显式例外。
func TestTestChannelByCredentialsKeepsCodexMultilineKey(t *testing.T) {
	key := "{\"access_token\":\"tok\",\n\"account_id\":\"acct\"}"
	got := credentialKeyFor(constant.ChannelTypeCodex, key)
	assert.Equal(t, key, got, "Codex 的 key 必须原样保留")

	got = credentialKeyFor(constant.ChannelTypeOpenAI, "sk-a\nsk-b")
	assert.Equal(t, "sk-a", got, "其它类型只取第一把")
}

// 新建弹窗要能选端点和测流式，`endpoint_type` / `stream` 必须与 GET /test/:id 同口径透传。
func TestTestChannelByCredentialsForwardsEndpointAndStream(t *testing.T) {
	for _, tc := range []struct {
		name     string
		extra    string
		content  string
		reply    string
		wantPath string
		wantBody string
	}{
		{
			name:     "stream",
			extra:    `,"stream":true`,
			content:  "text/event-stream",
			reply:    "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			wantPath: "/v1/chat/completions",
			wantBody: `"stream":true`,
		},
		{
			name:     "embeddings 端点",
			extra:    `,"endpoint_type":"embeddings"`,
			content:  "application/json",
			reply:    `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"m","usage":{"prompt_tokens":3,"total_tokens":3}}`,
			wantPath: "/v1/embeddings",
			wantBody: `"input"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previewTestEnv(t)
			// 流式要经过 StreamScannerHandler，它的 ticker 用这个超时（测试里默认 0 会 panic）。
			previousTimeout := constant.StreamingTimeout
			constant.StreamingTimeout = 30
			t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
			var seenPath string
			var seenBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenPath = r.URL.Path
				seenBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", tc.content)
				_, _ = w.Write([]byte(tc.reply))
			}))
			t.Cleanup(upstream.Close)

			body := `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-a","model":"m"` + tc.extra + `}`
			recorder, parsed := callTestByCredentials(t, body)
			upstream.Close()

			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, true, parsed["success"], "%v", parsed["message"])
			assert.Equal(t, tc.wantPath, seenPath, "端点选择必须作用到上游路径")
			assert.Contains(t, string(seenBody), tc.wantBody)
		})
	}
}
