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

// [FORK] 部门归属由专用接口（PUT /api/user/:id/department）单列维护。
// 其它管理动作走 UpdateWithTx 的整行 Updates，会把调用方内存里的旧快照按列回写。
func TestUpdateWithTxKeepsConcurrentDepartmentChange(t *testing.T) {
	setupUserUpdateTestState(t)
	require.NoError(t, DB.Exec("DELETE FROM department").Error)
	deptA := Department{Name: "竞态部门A"}
	require.NoError(t, DB.Save(&deptA).Error)
	deptB := Department{Name: "竞态部门B"}
	require.NoError(t, DB.Save(&deptB).Error)

	user := &User{
		Username: "dept-race", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		DepartmentId: &deptA.Id,
	}
	require.NoError(t, DB.Create(user).Error)

	// 管理员 B 读到旧快照（此时部门是 A）
	stale, err := GetUserById(user.Id, true)
	require.NoError(t, err)

	// 管理员 A 用专用接口把归属改成 B：单列更新，不读整行
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("department_id", deptB.Id).Error)

	// B 随后执行 disable，走 UpdateWithTx 的整行 Updates
	stale.Status = common.UserStatusDisabled
	require.NoError(t, stale.Update(false))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, got.Status)
	require.NotNil(t, got.DepartmentId)
	assert.Equal(t, deptB.Id, *got.DepartmentId, "整行更新不得把部门归属写回旧快照")
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
