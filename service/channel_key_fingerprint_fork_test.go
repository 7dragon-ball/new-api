package service

import (
	"strings"
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

// ---------------------------------------------------------------------------
// channel_key_masked：删除密钥后历史日志的归属快照
// ---------------------------------------------------------------------------

// 掩码与指纹同源同一次写：只写指纹的话，密钥一删历史行就再也算不出掩码。
func TestAppendChannelKeyFingerprintRecordsMaskedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "sk-abcdefghijklmnopqrstuvwxyz")

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sk-a**********wxyz", adminInfo["channel_key_masked"])
}

// 掩码快照与 BFF 的 maskTokenKey（packages/shared/src/mask.ts）必须逐字一致：
// 两侧口径分叉会让「已删除」判定永远不成立（BFF 拿快照比对不上自己算的掩码）。
// 这些向量与 BFF 的 mask 单测是同一张表，改口径时两边同时改。
func TestChannelKeyMaskedSnapshotVectors(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"1 位全星", "a", "*"},
		{"4 位全星", "abcd", "****"},
		{"5 位留前 2 后 2", "abcde", "ab****de"},
		{"8 位留前 2 后 2", "abcdefgh", "ab****gh"},
		{"9 位留前 4 后 4", "abcdefghi", "abcd**********fghi"},
		{"常见长度", "sk-abcdefghijklmnopqrstuvwxyz", "sk-a**********wxyz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, model.MaskTokenKey(c.key))
		})
	}
}

// 快照绝不落明文：拿整串去比对，任何长度都不允许相等。
// 用「露出位数 < 原文长度」而不是 `masked != key`：10–14 位的串若改用「前 10 后 4」
// 口径，前后缀会重叠、逐位可还原，但字符序列与原文不同，`!=` 抓不住。
func TestChannelKeyMaskedSnapshotNeverEqualsPlaintext(t *testing.T) {
	for _, key := range []string{
		"sk-abcdefghijklmnopqrstuvwxyz",
		"abcdefghij",   // 10 位：重叠口径的临界长度
		"abcdefghijkl", // 12 位
		"short",
	} {
		masked := model.MaskTokenKey(key)
		assert.NotEqual(t, key, masked, "掩码不得等于原文")
		revealed := strings.Count(masked, "*")
		assert.Less(t, len(masked)-revealed, len(key), "露出位数必须少于原文长度（%q → %q）", key, masked)
	}
}

// 插件托管的 credential 行同样只落脱敏串，不因为它是 JSON 就原样落库。
func TestAppendChannelKeyFingerprintMasksPluginCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	line := `{"type":"plugin_credential","plugin":"veo","token":"super-secret-oauth-token"}`
	ctx.Set(string(constant.ContextKeyChannelKey), line)

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	masked, _ := adminInfo["channel_key_masked"].(string)
	assert.NotEqual(t, line, masked)
	assert.NotContains(t, masked, "super-secret-oauth-token", "插件凭据的令牌不得进入日志")
}

// 没有 key 时两个字段都不写，保持「无值即缺字段」——不要写空串，BFF 用 `?? ` 会当成有值。
func TestAppendChannelKeyFingerprintOmitsMaskWithoutKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	adminInfo, _ := other.Snapshot()["admin_info"].(map[string]any)
	_, hasMask := adminInfo["channel_key_masked"]
	assert.False(t, hasMask)
}

// 掩码与指纹同作用域：都在 admin_info，不得漏进 public（public 对普通用户可见）。
func TestChannelKeyMaskedStaysInAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set(string(constant.ContextKeyChannelKey), "sk-abcdefghijklmnopqrstuvwxyz")

	other := model.NewLogOther()
	AppendChannelKeyFingerprint(ctx, other)

	snapshot := other.Snapshot()
	_, inPublic := snapshot["channel_key_masked"]
	assert.False(t, inPublic, "掩码不得出现在 public 顶层")
	adminInfo, ok := snapshot["admin_info"].(map[string]any)
	require.True(t, ok)
	_, inAdmin := adminInfo["channel_key_masked"]
	assert.True(t, inAdmin)
}
