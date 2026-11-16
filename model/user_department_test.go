package model

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserWithoutDepartmentSerializesNullNotMissingKey(t *testing.T) {
	// 前端按「缺值给 null、不是缺键」约定区分「未分配部门」与「字段没返回」。
	// 加 omitempty 会让 nil 指针的整个键消失，必须拦住。
	user := &User{Id: 1, Username: "u", DepartmentId: nil}
	payload, err := json.Marshal(user)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	value, ok := decoded["department_id"]
	assert.True(t, ok, "department_id 键必须存在，不能因 omitempty 消失")
	assert.Nil(t, value, "department_id 应为 null")
}

func TestUserDepartmentIdRoundTrips(t *testing.T) {
	truncateTables(t)
	id := 7
	user := &User{
		Username:     "dept-holder",
		Password:     "password123",
		Role:         common.RoleCommonUser,
		Status:       common.UserStatusEnabled,
		Group:        "default",
		AffCode:      "dept-aff",
		DepartmentId: &id,
	}
	require.NoError(t, DB.Create(user).Error)

	got, err := GetUserById(user.Id, false)
	require.NoError(t, err)
	require.NotNil(t, got.DepartmentId)
	assert.Equal(t, 7, *got.DepartmentId)
}

func TestUserDepartmentIdCanBeClearedToNull(t *testing.T) {
	truncateTables(t)
	id := 9
	user := &User{
		Username:     "dept-clear",
		Password:     "password123",
		Role:         common.RoleCommonUser,
		Status:       common.UserStatusEnabled,
		Group:        "default",
		AffCode:      "clear-aff",
		DepartmentId: &id,
	}
	require.NoError(t, DB.Create(user).Error)

	// 零值 int 会被 GORM 跳过，只有 *int 的 nil 才能真正写成 NULL。
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("department_id", (*int)(nil)).Error)

	got, err := GetUserById(user.Id, false)
	require.NoError(t, err)
	assert.Nil(t, got.DepartmentId)
}
