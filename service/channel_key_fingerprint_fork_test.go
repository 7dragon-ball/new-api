package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gin-gonic/gin"
)

// 与 bff/src/modules/logs/logs.service.spec.ts 的 CHANNEL_KEY_FP_VECTORS 是同一张表：
// 两侧任何一处改了哈希口径，这里或那里就会红。改算法时两边同时改。
//
// 哈希输入不做任何规范化。Go 的 TrimSpace 与 JS 的 trim() 覆盖的字符集不同
// （U+FEFF 对 JS 是空白、对 Go 不是；U+0085 反过来），加 trim 只会制造不一致。
// 两侧输入本来就是同一个字符串，不 trim 是唯一安全的做法。
func TestChannelKeyFingerprintVectors(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"普通串", "sk-abc123", "0a2f6ba7b3d49736"},
		{"首尾空白原样参与哈希", "  sk-abc123\t", "f7a9e6d8c125e352"},
		{"内部换行保留", "line-one\nline-two", "7a292ccd133e5f17"},
		{"内部 CRLF 保留", "internal\r\nline", "bb05836117426a07"},
		{"中间空行保留", "a\n\nb", "38022fd2b8dbc5cb"},
		{"单密钥", "single-key-secret-123456", "9f431373b2e17fc0"},
		{"多密钥的一行", "efg-secret-value-123456", "296de5b0c20acf93"},
		{"多行 JSON 整体参与哈希", "{\n  \"type\": \"service_account\",\n  \"project_id\": \"p\"\n}", "a29ce732f7095b71"},
		{"BOM 原样保留（JS 会剥掉，故必须不 trim）", string(rune(0xFEFF)) + "sk-abc123", "bb4ca06dfa4a97eb"},
		{"NEL 原样保留（Go 的 TrimSpace 会剥掉，故必须不 trim）", string(rune(0x85)) + "sk-abc123", "688f665d8da27346"},
		{"空串返回空", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, ChannelKeyFingerprint(c.key))
		})
	}
}

// 纯空白串不是空串：不做 trim 之后它照样参与哈希，结果必须与 "" 不同。
func TestChannelKeyFingerprintKeepsWhitespaceOnlyInput(t *testing.T) {
	assert.NotEqual(t, "", ChannelKeyFingerprint("   \n\t "))
	assert.Equal(t, "0a2f6ba7b3d49736", ChannelKeyFingerprint("sk-abc123"))
}

// 上下文里的 key 是日志写入时刻实际发出去的那一行。插件渠道刷新后 ContextKeyChannelKey
// 会被改写成落库的新行，这里钉住「取上下文，不取 GetNextEnabledKey 的旧值」。
func TestAppendChannelKeyFingerprintUsesContextKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "refreshed-line-after-plugin-refresh")

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info 应被创建")
	assert.Equal(t, ChannelKeyFingerprint("refreshed-line-after-plugin-refresh"), adminInfo["channel_key_fp"])
}

func TestAppendChannelKeyFingerprintSkipsWhenKeyMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	snapshot := other.Snapshot()
	adminInfo, _ := snapshot["admin_info"].(map[string]any)
	_, exists := adminInfo["channel_key_fp"]
	assert.False(t, exists, "没有 key 时不应写该字段，保持「无值即缺字段」")
}

func TestAppendChannelKeyFingerprintIgnoresNilOther(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "sk-abc123")

	assert.NotPanics(t, func() { AppendChannelKeyFingerprint(ctx, nil) })
}

// 只写 admin_info，不进 public：public 对普通用户可见，指纹必须留在管理员作用域。
func TestAppendChannelKeyFingerprintStaysInAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "sk-abc123")

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	snapshot := other.Snapshot()
	_, inPublic := snapshot["channel_key_fp"]
	assert.False(t, inPublic, "指纹不得出现在 public 顶层")
}
