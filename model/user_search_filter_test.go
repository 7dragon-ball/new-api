package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertFilterUser(t *testing.T, username, displayName string, deptId *int) *User {
	t.Helper()
	user := &User{
		Username: username, Password: "password123",
		DisplayName: displayName, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default",
		AffCode: username + "-aff", DepartmentId: deptId,
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func TestSearchUsersFiltersByDepartmentId(t *testing.T) {
	truncateTables(t)
	deptId := 3
	insertFilterUser(t, "in-dept", "在部门", &deptId)
	insertFilterUser(t, "no-dept", "无部门", nil)

	users, total, err := SearchUsers("", "", nil, nil, &deptId, "", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, users, 1)
	assert.Equal(t, "in-dept", users[0].Username)
}

func TestSearchUsersUsernameIsExactMatch(t *testing.T) {
	truncateTables(t)
	insertFilterUser(t, "alice", "Alice", nil)
	insertFilterUser(t, "alicia", "Alicia", nil)

	users, total, err := SearchUsers("", "", nil, nil, nil, "alice", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "精确匹配不能带出 alicia")
	require.Len(t, users, 1)
	assert.Equal(t, "alice", users[0].Username)
}

func TestSearchUsersDisplayNameIsExactMatch(t *testing.T) {
	truncateTables(t)
	insertFilterUser(t, "zs1", "张三", nil)
	insertFilterUser(t, "zs2", "张三丰", nil)

	users, total, err := SearchUsers("", "", nil, nil, nil, "", "张三", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, users, 1)
	assert.Equal(t, "zs1", users[0].Username)
}

func TestSearchUsersDefaultExcludesSoftDeletedFromItemsAndTotal(t *testing.T) {
	truncateTables(t)
	insertFilterUser(t, "alive", "活着", nil)
	gone := insertFilterUser(t, "gone", "已注销", nil)
	require.NoError(t, DB.Delete(&User{}, gone.Id).Error)

	users, total, err := SearchUsers("", "", nil, nil, nil, "", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "total 必须与 items 同源，否则列表出现空页")
	assert.Len(t, users, 1)
	assert.Equal(t, "alive", users[0].Username)
}

func TestSearchUsersStatusMinusOneReturnsOnlySoftDeleted(t *testing.T) {
	truncateTables(t)
	insertFilterUser(t, "alive2", "活着", nil)
	gone := insertFilterUser(t, "gone2", "已注销", nil)
	require.NoError(t, DB.Delete(&User{}, gone.Id).Error)

	minusOne := -1
	users, total, err := SearchUsers("", "", nil, &minusOne, nil, "", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, users, 1)
	assert.Equal(t, "gone2", users[0].Username)
}

func TestSearchUsersStatusEnabledExcludesSoftDeleted(t *testing.T) {
	truncateTables(t)
	insertFilterUser(t, "enabled-one", "启用", nil)
	gone := insertFilterUser(t, "deleted-one", "已注销", nil)
	require.NoError(t, DB.Delete(&User{}, gone.Id).Error)

	enabled := common.UserStatusEnabled
	_, total, err := SearchUsers("", "", nil, &enabled, nil, "", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
}
