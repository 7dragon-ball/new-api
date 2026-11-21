package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// previewTestEnv 准备一个最小可用的渠道测试环境：内存 sqlite + 一个 root 用户 + 已初始化的 i18n。
// i18n 必须在骗过定价校验失败分支之前初始化，否则 modelPriceNotConfiguredError 会 nil deref。
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

// 模型在网关没有配置 ratio 时，预览态也必须能把请求真正发出去。
func TestPreviewTestChannelSkipsPriceCheck(t *testing.T) {
	previewTestEnv(t)
	var hits int
	upstream := chatOK(t, &hits)
	defer upstream.Close()

	base := upstream.URL
	ch := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "sk-preview", BaseURL: &base}

	result := testChannel(t.Context(), ch, 7, "gpt-4o-mini", "", false)

	require.Nil(t, result.localErr, "预览态不应因模型未定价而失败：%v", result.localErr)
	require.Nil(t, result.newAPIError)
	assert.Equal(t, 1, hits, "请求必须真的打到上游")
}

// 预览态不写 consumer log（Id=0 会让它记到 channel 0）。
//
// 这里必须给模型配一个 ratio：否则请求在 ModelPriceHelper 就被挡下，永远走不到
// RecordConsumeLog，断言 channel 0 没有日志会「因为没跑到」而通过，测不出任何东西。
//
// 注意：`testChannel()` 自身不调 `UpdateResponseTime`（那是 handler 的事），所以本任务
// 只钉消费日志。「不改 channels 表」那条断言属于 Task 2 的 handler 测试。
func TestPreviewTestChannelWritesNoConsumeLog(t *testing.T) {
	previewTestEnv(t)
	pricedModel(t, "preview-priced-model")
	upstream := chatOK(t, nil)
	defer upstream.Close()

	base := upstream.URL
	ch := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "sk-preview", BaseURL: &base}
	_ = testChannel(t.Context(), ch, 7, "preview-priced-model", "", false)

	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count, "预览态不得写任何消费日志")
}

// pricedModel 给模型配一个 ratio，让测试能越过定价校验走到发请求与结算那一步。
func pricedModel(t *testing.T, name string) {
	t.Helper()
	previous := ratio_setting.GetModelRatioCopy()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+name+`":1}`))
	t.Cleanup(func() {
		encoded, err := common.Marshal(previous)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
	})
}

// 已保存渠道（Id > 0）行为不变：仍然走定价校验。
func TestSavedChannelStillChecksPrice(t *testing.T) {
	previewTestEnv(t)
	var hits int
	upstream := chatOK(t, &hits)
	defer upstream.Close()

	base := upstream.URL
	ch := &model.Channel{Id: 42, Type: constant.ChannelTypeOpenAI, Key: "sk-saved", BaseURL: &base}

	result := testChannel(t.Context(), ch, 7, "unpriced-model-xyz", "", false)

	require.NotNil(t, result.localErr, "已保存渠道未定价时必须失败")
	assert.Zero(t, hits, "定价校验在发请求之前，上游不应被调用")
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-first", r.Header.Get("Authorization"), "多行秘钥只取第一把")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	body := `{"type":1,"base_url":"` + upstream.URL + `","key":"sk-first\nsk-second","model":"gpt-4o-mini"}`
	recorder, parsed := callTestByCredentials(t, body)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, true, parsed["success"])
	assert.Equal(t, 1, hits)
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

// 「自建算力」渠道的秘钥可以留空（内网网关不校验），预览测试不能把它挡在 400。
func TestTestChannelByCredentialsAllowsEmptyKey(t *testing.T) {
	previewTestEnv(t)
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
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
