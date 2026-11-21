package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
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
