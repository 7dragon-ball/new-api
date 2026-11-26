package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// 二开：预扣费/额度不足这类错误显式带了 types.ErrOptionWithNoRecordErrorLog，
// 会被 RecordRelayErrorLog 主动跳过，导致使用日志看不到这类失败态。这里清除该抑制
// 标记后按原错误对象落库，保留其 error_type/RelayError/error_code/status_code 等全部
// 信息（仍受 constant.ErrorLogEnabled 开关控制）。
func ForceRecordRelayErrorLog(c *gin.Context, channelId int, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	err.SetRecordErrorLog(true)
	RecordRelayErrorLog(c, channelId, err, relayInfo)
}
