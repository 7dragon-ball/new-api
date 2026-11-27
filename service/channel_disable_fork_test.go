package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 自动停用的白名单：只有「秘钥失效」与「秘钥全部停用」两类 channel:* 错误
// 属于秘钥问题；其余 channel:* 是渠道配置错误（参数覆盖 / header / 模型映射 / AWS 适配器 /
// 测试超时），停用后不会自愈，必须由人改配置。上游把它们和秘钥错误一起无条件停用，
// 产品要求「只有五类可以自动停用」，故收窄。
func TestShouldDisableChannelChannelErrorWhitelist(t *testing.T) {
	prev := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prev })

	t.Run("秘钥类 channel:* 仍然停用", func(t *testing.T) {
		cases := []struct {
			name string
			err  *types.NewAPIError
		}{
			{"invalid_key", types.NewError(errors.New("invalid auth"), types.ErrorCodeChannelInvalidKey)},
			{"no_available_key", types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)},
		}
		for _, c := range cases {
			assert.True(t, ShouldDisableChannel(c.err), "%s 应当停用", c.name)
		}
	})

	t.Run("配置类 channel:* 不再停用", func(t *testing.T) {
		cases := []struct {
			name string
			err  *types.NewAPIError
		}{
			{"param_override_invalid", types.NewError(errors.New("bad override"), types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())},
			{"header_override_invalid", types.NewError(errors.New("bad header"), types.ErrorCodeChannelHeaderOverrideInvalid)},
			{"model_mapped_error", types.NewError(errors.New("bad mapping"), types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())},
			{"aws_client_error", types.NewError(errors.New("aws rejected"), types.ErrorCodeChannelAwsClientError)},
			{"response_time_exceeded", types.NewOpenAIError(errors.New("slow"), types.ErrorCodeChannelResponseTimeExceeded, 408)},
		}
		for _, c := range cases {
			assert.False(t, ShouldDisableChannel(c.err), "%s 不该停用渠道", c.name)
		}
	})

	t.Run("配置类返回的报文仍按原样透出，不吞错", func(t *testing.T) {
		// 不停用不等于不报错：请求仍带着上游原文失败，只是渠道不进停用状态
		err := types.NewError(errors.New("bad override"), types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
		require.False(t, ShouldDisableChannel(err))
		assert.Contains(t, err.Error(), "bad override")
	})

	// `param_override` 的错误可能不带 skipRetry（不是所有构造点都加了它）。这类错误若
	// 落到后面的判据，就会因为自定义的 401 状态码或额度类文案被停用。下面两条用**不带
	// skipRetry** 的配置类错误，保证测试真的区分「直接返回 false」与「返回 true / fall-through」。
	t.Run("不带 skipRetry 的配置类错误，即使状态码是 401 也不停用", func(t *testing.T) {
		err := types.NewError(
			errors.New("bad override"),
			types.ErrorCodeChannelParamOverrideInvalid,
			types.ErrOptionWithStatusCode(401),
		)
		assert.False(t, ShouldDisableChannel(err), "配置类错误不该因为状态码是 401 被停用")
	})

	t.Run("不带 skipRetry 的配置类错误，即使文本像额度问题也不停用", func(t *testing.T) {
		err := types.NewError(
			errors.New("You exceeded your current quota"),
			types.ErrorCodeChannelParamOverrideInvalid,
		)
		assert.False(t, ShouldDisableChannel(err), "配置类错误不该因为文案像额度问题被停用")
	})
}

// 非 channel:* 的路径不受本次改动影响：状态码 401 与关键词仍照原样生效。
func TestShouldDisableChannelStatusAndKeywordUnchanged(t *testing.T) {
	prevEnabled := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prevEnabled })

	assert.True(t, ShouldDisableChannel(
		types.NewErrorWithStatusCode(errors.New("Incorrect API key provided"), types.ErrorCodeBadResponseStatusCode, 401),
	), "401 应当停用")

	assert.False(t, ShouldDisableChannel(
		types.NewErrorWithStatusCode(errors.New("too many requests"), types.ErrorCodeBadResponseStatusCode, 429),
	), "429 默认不应当停用")

	assert.True(t, ShouldDisableChannel(
		types.NewErrorWithStatusCode(errors.New("You exceeded your current quota"), types.ErrorCodeBadResponseStatusCode, 429),
	), "命中额度关键词时应当停用")

	assert.False(t, ShouldDisableChannel(nil), "nil 错误不应当停用")

	common.AutomaticDisableChannelEnabled = false
	assert.False(t, ShouldDisableChannel(
		types.NewError(errors.New("invalid auth"), types.ErrorCodeChannelInvalidKey),
	), "总开关关闭时连秘钥错误也不停用")
}
