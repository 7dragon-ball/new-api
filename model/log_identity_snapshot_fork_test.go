package model

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 身份快照必须来自鉴权上下文而不是回查数据库：每次调用都写日志，多一次查库就是延迟。
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
	// 账号与显示名 / 部门同源：都是鉴权上下文里的值，不回查 users
	assert.Equal(t, "ctx-user", got.Username)
	assert.Equal(t, "上下文显示名", got.DisplayName)
	assert.Equal(t, "上下文部门", got.Department)
}

// 渠道名必须写入时快照：上游把这列标成 `gorm:"->"`（只读），名字只靠查询时反查
// channels 表补；渠道是硬删除，删掉后历史日志的渠道名就永久丢了。
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

// 供应商（channels.tag）与秘钥使用方（tokens.owner）同理：反查只能看到现在还在的，
// 所以写入时从上下文抄一份。
func TestRecordConsumeLogSnapshotsVendorAndTokenOwner(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	})
	prev := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = prev })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("id", 999999)
	// distributor 写的供应商名（channels.tag）与 auth 写的使用方（tokens.owner）
	c.Set(string(constant.ContextKeyChannelTag), "OpenAI")
	c.Set(string(constant.ContextKeyTokenOwner), "算法组")

	RecordConsumeLog(c, 999999, RecordConsumeLogParams{ModelName: "test-model", Quota: 1})

	var got Log
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Equal(t, "OpenAI", got.ChannelTag, "供应商名必须落库，否则渠道删掉后历史日志筛不到")
	assert.Equal(t, "算法组", got.TokenOwner, "使用方必须落库，否则令牌删掉后历史日志筛不到")
}

// 上下文里没有这两项时（系统任务、渠道测试）留空，不回查库。
func TestRecordConsumeLogLeavesVendorAndOwnerEmptyWithoutContext(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	})
	prev := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = prev })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("id", 999999)

	RecordConsumeLog(c, 999999, RecordConsumeLogParams{ModelName: "test-model", Quota: 1})

	var got Log
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Empty(t, got.ChannelTag)
	assert.Empty(t, got.TokenOwner)
}

// 读取侧：渠道被删后反查不到名字，日志列表与导出流都必须退回行内快照。
func TestReadPathsKeepChannelSnapshotAfterChannelDeleted(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	clear := func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	}
	clear()
	t.Cleanup(clear)
	// 走内存渠道缓存（里面没有这个渠道）：sqlite 测试库是单连接，导出流边遍历结果集
	// 边反查渠道，回查库会死锁。
	prevMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = prevMemoryCache })
	// channels 表里没有 987654 这一行：模拟渠道已被硬删除
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:      424242,
		ChannelId:   987654,
		ChannelName: "已删渠道",
		Type:        LogTypeConsume,
		CreatedAt:   common.GetTimestamp(),
	}).Error)

	logs, _, err := GetAllLogs(LogFilter{Type: LogTypeConsume}, 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, "已删渠道", logs[0].ChannelName, "分页列表不能用空串盖掉行内快照")

	var streamed []string
	require.NoError(t, StreamAllLogs(context.Background(), LogFilter{Type: LogTypeConsume}, common.RoleRootUser, func(log *Log) error {
		streamed = append(streamed, log.ChannelName)
		return nil
	}))
	assert.Equal(t, []string{"已删渠道"}, streamed, "导出流同样不能用空串盖掉行内快照")
}

// 用户导出与用户列表是同一条用户可见读取路径：管理侧口径的渠道名 / 供应商 / 使用方
// 在两边都必须被抹掉，否则导出的 JSON 行比列表多带两个字段。
func TestStreamUserLogsStripsAdminOnlySnapshotFields(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	clear := func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	}
	clear()
	t.Cleanup(clear)

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:      616161,
		ChannelId:   987654,
		ChannelName: "已删渠道",
		ChannelTag:  "OpenAI",
		TokenOwner:  "算法组",
		Type:        LogTypeConsume,
		CreatedAt:   common.GetTimestamp(),
		Other:       common.MapToJsonStr(map[string]any{"model_price": 1.5, "admin_info": map[string]any{"reject_reason": "policy"}}),
	}).Error)

	var streamed []Log
	require.NoError(t, StreamUserLogs(context.Background(), 616161, LogFilter{Type: LogTypeConsume}, func(log *Log) error {
		streamed = append(streamed, *log)
		return nil
	}))
	require.Len(t, streamed, 1)
	assert.Empty(t, streamed[0].ChannelName)
	assert.Empty(t, streamed[0].ChannelTag, "普通用户的导出不能带渠道供应商")
	assert.Empty(t, streamed[0].TokenOwner, "普通用户的导出不能带秘钥使用方")
	assert.NotContains(t, streamed[0].Other, "admin_info")
}

// 上下文里的身份属于「当前登录者」：管理员代他人写日志时不能照抄，否则等于把操作者的
// 部门记在别人名下。
func TestRecordLogWithAdminInfoRejectsForeignIdentity(t *testing.T) {
	require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		_ = LOG_DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Log{}).Error
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("id", 1)
	c.Set(string(constant.ContextKeyUserDisplayName), "操作者")
	c.Set(string(constant.ContextKeyUserDepartmentName), "运营部")

	// 日志归属他人：上下文里的身份不属于这个 userId
	RecordLogWithAdminInfo(2, LogTypeSystem, "system op", nil, nil, c)
	var got Log
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Equal(t, 2, got.UserId)
	assert.Empty(t, got.DisplayName)
	assert.Empty(t, got.Department)

	// 同一个人：正常写入
	got = Log{} // 上一次查询回填了 Id，不清零会变成按主键再查同一条
	RecordLogWithAdminInfo(1, LogTypeSystem, "system op", nil, nil, c)
	require.NoError(t, LOG_DB.Order("id desc").First(&got).Error)
	assert.Equal(t, "操作者", got.DisplayName)
	assert.Equal(t, "运营部", got.Department)
}

// 后台任务计费没有请求上下文，仍要按 userId 从用户缓存取到身份：身份是 quota_data
// 聚合键的一部分，留空会让任务用量与同时段的同步调用拆成两行。
func TestRecordTaskBillingLogSnapshotsCachedIdentity(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	resetQuotaDataState(t)
	prevConsume, prevExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled = true
	common.DataExportEnabled = true
	t.Cleanup(func() {
		common.LogConsumeEnabled = prevConsume
		common.DataExportEnabled = prevExport
	})

	department := &Department{Name: "算法部"}
	require.NoError(t, DB.Create(department).Error)
	departmentId := department.Id
	user := User{
		Username:     "task-user",
		DisplayName:  "任务用户",
		Group:        "default",
		Role:         common.RoleCommonUser,
		Status:       common.UserStatusEnabled,
		AuthVersion:  1,
		DepartmentId: &departmentId,
	}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, populateUserCache(user))

	RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    user.Id,
		LogType:   LogTypeConsume,
		Content:   []*common.Message{common.NewMessage("task billing")},
		ModelName: "task-model",
		Quota:     1,
		Group:     "default",
		Other:     NewLogOther(),
	})

	var logRow Log
	require.NoError(t, LOG_DB.Order("id desc").First(&logRow).Error)
	assert.Equal(t, "任务用户", logRow.DisplayName)
	assert.Equal(t, "算法部", logRow.Department)

	CacheQuotaDataLock.Lock()
	defer CacheQuotaDataLock.Unlock()
	require.Len(t, CacheQuotaData, 1)
	for _, quotaData := range CacheQuotaData {
		assert.Equal(t, "任务用户", quotaData.DisplayName)
		assert.Equal(t, "算法部", quotaData.Department)
	}
}
