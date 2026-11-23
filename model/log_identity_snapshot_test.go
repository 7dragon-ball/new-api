package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 写日志的身份快照必须来自鉴权上下文，不能回查数据库：
// 每次 LLM 调用都写日志，多一次查库就是一次可观测的延迟。
func TestRecordConsumeLogUsesContextIdentity(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	})
	// 不清这个全局开关，日志根本不会被写，测试会「假绿」
	prev := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = prev })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// 库里没有这个用户：若实现回查数据库，快照就会是空，测试随即失败
	c.Set("id", 999999)
	c.Set(string(constant.ContextKeyUserName), "ctx-user")
	c.Set(string(constant.ContextKeyUserDisplayName), "上下文显示名")
	c.Set(string(constant.ContextKeyUserDepartmentName), "上下文部门")

	RecordConsumeLog(c, 999999, RecordConsumeLogParams{ModelName: "test-model", Quota: 1})

	var got Log
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Equal(t, "上下文显示名", got.DisplayName)
	assert.Equal(t, "上下文部门", got.Department)
}

// 渠道名必须写入时快照：渠道只删不改，删除后没有别的地方能查到这个名字。
//
// 上游把 `ChannelName` 标成了 `gorm:"->"`（只读），所以这一列从来没被写过，
// 新日志恒为空；而 BFF 的渠道名筛选正是按这一列 ILIKE，导致筛选也永远搜不到。
func TestRecordConsumeLogSnapshotsChannelName(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	})
	prev := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = prev })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("id", 999999)
	c.Set(string(constant.ContextKeyUserName), "ctx-user")
	// 分发阶段写入上下文的渠道名（middleware/distributor.go）
	c.Set(string(constant.ContextKeyChannelName), "Mock 双密钥")

	RecordConsumeLog(c, 999999, RecordConsumeLogParams{ModelName: "test-model", Quota: 1, ChannelId: 8})

	var got Log
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Equal(t, "Mock 双密钥", got.ChannelName, "渠道名必须落库；只读字段会让筛选永远搜不到")
}
