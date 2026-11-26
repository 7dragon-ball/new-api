package service

import (
	"errors"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// 二开：预扣费/额度不足这类错误显式带了 types.ErrOptionWithNoRecordErrorLog，
// 会被 RecordRelayErrorLog 主动跳过，导致使用日志看不到这类失败态。这里复用同一套
// 写库逻辑，仅重建一个不带抑制标记的等价错误后再落一条错误日志（元信息、error_type
// 保持一致；仍受 constant.ErrorLogEnabled 开关控制）。
func ForceRecordRelayErrorLog(c *gin.Context, channelId int, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	message := err.Error()
	if err.Err != nil {
		message = err.Err.Error()
	}
	loggable := types.NewErrorWithStatusCode(errors.New(message), err.GetErrorCode(), err.StatusCode)
	RecordRelayErrorLog(c, channelId, loggable, relayInfo)
}
