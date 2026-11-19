package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gin-gonic/gin"
)

// 成功与失败两类日志都走 AppendRelayLogAdminInfo，指纹必须在里面写，
// 不能只在消费日志路径补。
func TestAppendRelayLogAdminInfoRecordsChannelKeyFingerprint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "sk-abc123")

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "0a2f6ba7b3d49736", adminInfo["channel_key_fp"])
}

// 无 key 的上下文（例如请求在选渠道前就被拒绝）不写该字段。
func TestAppendRelayLogAdminInfoOmitsFingerprintWithoutKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)

	adminInfo, _ := other.Snapshot()["admin_info"].(map[string]any)
	_, exists := adminInfo["channel_key_fp"]
	assert.False(t, exists)
}

// 指纹与 multi_key_index 同时存在：前者是稳定标识，后者供界面显示「Key N」，互不替代。
func TestAppendRelayLogAdminInfoKeepsBothFingerprintAndIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "efg-secret-value-123456")
	ctx.Set(string(constant.ContextKeyChannelIsMultiKey), true)
	ctx.Set(string(constant.ContextKeyChannelMultiKeyIndex), 1)

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "296de5b0c20acf93", adminInfo["channel_key_fp"])
	assert.Equal(t, 1, adminInfo["multi_key_index"])
	assert.Equal(t, true, adminInfo["is_multi_key"])
}

// 单密钥渠道上下文里 IsMultiKey 为 false：没有序号字段，但指纹照样要有——
// 否则单密钥渠道的日志完全无法按密钥反查。
func TestAppendRelayLogAdminInfoFingerprintOnSingleKeyChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "single-key-secret-123456")
	ctx.Set(string(constant.ContextKeyChannelIsMultiKey), false)

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "9f431373b2e17fc0", adminInfo["channel_key_fp"])
	_, hasIndex := adminInfo["multi_key_index"]
	assert.False(t, hasIndex, "单密钥渠道上游不写 multi_key_index")
}
