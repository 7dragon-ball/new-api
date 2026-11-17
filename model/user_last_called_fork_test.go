package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 最近调用时刻：非批量路径应写进 users.last_called_at
func TestUpdateUserUsedQuotaAndRequestCountTouchesLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_nonbatch", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
	}
	require.NoError(t, DB.Create(user).Error)
	require.Equal(t, int64(0), user.LastCalledAt)

	before := common.GetTimestamp()
	UpdateUserUsedQuotaAndRequestCount(user.Id, 100)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.GreaterOrEqual(t, got.LastCalledAt, before, "应写入不早于调用时刻的时间戳")
	assert.Equal(t, 100, got.UsedQuota)
	assert.Equal(t, 1, got.RequestCount)
}

// [FORK] UpdateUserUsedQuota 只被退款/结算调用，代表「抵扣回退」而不是调用
func TestUpdateUserUsedQuotaDoesNotTouchLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_refund", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		LastCalledAt: 1700000000,
	}
	require.NoError(t, DB.Create(user).Error)

	// RefundTaskQuota / RefundMidjourneyQuota 全额退款走这里
	UpdateUserUsedQuota(user.Id, -50)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, -50, got.UsedQuota)
	assert.Equal(t, int64(1700000000), got.LastCalledAt, "退款不是调用，不得刷新最近调用时刻")
}

// [FORK] 批量刷新落点同样要写
func TestBatchFlushTouchesLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_batch", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
	}
	require.NoError(t, DB.Create(user).Error)

	before := common.GetTimestamp()
	updateUserQuotaUsedQuotaAndRequestCount(user.Id, 0, 70, 2)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.GreaterOrEqual(t, got.LastCalledAt, before)
	assert.Equal(t, 70, got.UsedQuota)
}

// [FORK] 批量模式下，纯额度变更（管理员充值 / 兑换码）不得算作一次调用
func TestBatchFlushQuotaOnlyDoesNotTouchLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_balance", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		LastCalledAt: 1700000000,
	}
	require.NoError(t, DB.Create(user).Error)

	// IncreaseUserQuota(..., db=false) 在批量模式下只入队 BatchUpdateTypeUserQuota
	updateUserQuotaUsedQuotaAndRequestCount(user.Id, 500, 0, 0)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 500, got.Quota)
	assert.Equal(t, int64(1700000000), got.LastCalledAt, "充值不是调用，不得刷新最近调用时刻")
}

// [FORK] 批量模式下，退款结算（只减 used_quota、不动请求数）不得算作一次调用
func TestBatchFlushRefundDoesNotTouchLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_batch_refund", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		LastCalledAt: 1700000000,
	}
	require.NoError(t, DB.Create(user).Error)

	// RefundTaskQuota 在批量模式下只入队 BatchUpdateTypeUsedQuota，请求数为 0
	updateUserQuotaUsedQuotaAndRequestCount(user.Id, 0, -70, 0)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, -70, got.UsedQuota)
	assert.Equal(t, int64(1700000000), got.LastCalledAt, "退款不是调用，不得刷新最近调用时刻")
}

// [FORK] 编辑用户（整行覆盖）不得改写 last_called_at
func TestUpdateWithTxKeepsLastCalledAt(t *testing.T) {
	truncateTables(t)
	user := &User{
		Username: "lc_edit", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		LastCalledAt: 1700000000,
	}
	require.NoError(t, DB.Create(user).Error)

	// 模拟从库里读出来、改了 display_name 再存回（老快照 LastCalledAt 仍是旧值）
	stale := *user
	stale.DisplayName = "改名"
	stale.LastCalledAt = 1 // 故意塞一个旧值
	require.NoError(t, stale.Update(false))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, int64(1700000000), got.LastCalledAt, "编辑不得覆盖最近调用时刻")
	assert.Equal(t, "改名", got.DisplayName)
}
