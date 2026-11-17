package controller

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustDept(t *testing.T, name string) model.Department {
	t.Helper()
	dept := model.Department{Name: name}
	require.NoError(t, model.DB.Save(&dept).Error)
	return dept
}

func mustUserInDept(t *testing.T, username string, deptId *int) *model.User {
	t.Helper()
	user := &model.User{
		Username: username, Password: "password123", DisplayName: username,
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		AffCode: username + "-aff", DepartmentId: deptId,
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func mustToken(t *testing.T, user *model.User, name string) *model.Token {
	t.Helper()
	token := &model.Token{UserId: user.Id, Name: name, Key: "key-" + name + strconv.Itoa(user.Id), Status: common.TokenStatusEnabled}
	require.NoError(t, model.DB.Create(token).Error)
	return token
}

func mustQuotaData(t *testing.T, user *model.User, createdAt int64, count, quota, tokenUsed int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID: user.Id, Username: user.Username, ModelName: "m",
		CreatedAt: createdAt, Count: count, Quota: quota, TokenUsed: tokenUsed,
	}).Error)
}

func setupDepartmentUsageTest(t *testing.T) {
	t.Helper()
	_, _ = setupUserSearchControllerTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.QuotaData{}))
	require.NoError(t, model.DB.Exec("DELETE FROM tokens").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM quota_data").Error)
}

func TestDepartmentUsageAggregatesWithoutMultiplying(t *testing.T) {
	setupDepartmentUsageTest(t)
	deptA := mustDept(t, "甲部门")
	deptB := mustDept(t, "乙部门")
	mustDept(t, "零成员部门")

	// 甲：2 人。alice 3 把秘钥 + 2 条流水；bob 无秘钥无流水
	alice := mustUserInDept(t, "usage-alice", &deptA.Id)
	mustUserInDept(t, "usage-bob", &deptA.Id)
	mustToken(t, alice, "a1")
	mustToken(t, alice, "a2")
	mustToken(t, alice, "a3")
	mustQuotaData(t, alice, 1000, 4, 40, 400)
	mustQuotaData(t, alice, 2000, 6, 60, 600)

	// 乙：1 人，区间内无流水
	carol := mustUserInDept(t, "usage-carol", &deptB.Id)
	mustToken(t, carol, "c1")

	response, resp := departmentRequest(t, http.MethodGet,
		"/api/data/departments?start_timestamp=1&end_timestamp=9999", "", nil, GetDepartmentUsage)
	assert.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	rows, ok := resp["data"].([]any)
	require.True(t, ok, "data 必须是数组，实际=%T", resp["data"])

	byName := map[string]map[string]any{}
	for _, raw := range rows {
		row := raw.(map[string]any)
		byName[row["name"].(string)] = row
	}
	require.Contains(t, byName, "甲部门")
	require.Contains(t, byName, "乙部门")
	require.Contains(t, byName, "零成员部门", "零成员部门也要出现（原型要求先建桶）")

	a := byName["甲部门"]
	assert.EqualValues(t, 2, a["members"])
	assert.EqualValues(t, 3, a["keys"])
	assert.EqualValues(t, 10, a["calls"])
	// 3 把秘钥 × 2 条流水直接平铺 JOIN 会变成 6 行，sum 放大 3 倍 —— 这里是真实值
	assert.EqualValues(t, 100, a["quota"], "多秘钥不得放大 sum")
	assert.EqualValues(t, 1000, a["tokens"], "多秘钥不得放大 sum")

	b := byName["乙部门"]
	assert.EqualValues(t, 1, b["members"])
	assert.EqualValues(t, 1, b["keys"])
	assert.EqualValues(t, 0, b["calls"])
	assert.EqualValues(t, 0, b["quota"])

	empty := byName["零成员部门"]
	assert.EqualValues(t, 0, empty["members"])
	assert.EqualValues(t, 0, empty["keys"])
	assert.EqualValues(t, 0, empty["quota"])

	// 按用量从高到低
	assert.Equal(t, "甲部门", rows[0].(map[string]any)["name"])
}

func TestDepartmentUsageIgnoresSoftDeletedUsersAndTokens(t *testing.T) {
	setupDepartmentUsageTest(t)
	dept := mustDept(t, "丙部门")
	gone := mustUserInDept(t, "usage-gone", &dept.Id)
	mustToken(t, gone, "g1")
	mustQuotaData(t, gone, 1000, 5, 50, 500)
	require.NoError(t, model.DB.Delete(&model.User{}, gone.Id).Error)

	_, resp := departmentRequest(t, http.MethodGet,
		"/api/data/departments?start_timestamp=1&end_timestamp=9999", "", nil, GetDepartmentUsage)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	rows := resp["data"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.EqualValues(t, 0, row["members"], "软删用户不计入成员数")
	assert.EqualValues(t, 0, row["keys"], "软删用户的秘钥不计入")
}

func TestDepartmentUsageExcludesUsersWithoutDepartment(t *testing.T) {
	setupDepartmentUsageTest(t)
	dept := mustDept(t, "丁部门")
	mustToken(t, mustUserInDept(t, "usage-homeless", nil), "h1")
	mustQuotaData(t, mustUserInDept(t, "usage-homeless2", nil), 1000, 9, 90, 900)

	_, resp := departmentRequest(t, http.MethodGet,
		"/api/data/departments?start_timestamp=1&end_timestamp=9999", "", nil, GetDepartmentUsage)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	rows := resp["data"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.EqualValues(t, dept.Id, row["id"])
	assert.EqualValues(t, 0, row["members"])
	assert.EqualValues(t, 0, row["calls"], "未分配部门的用户不得落进任何部门行")
}

func TestDepartmentUsageExcludesOutOfRangeData(t *testing.T) {
	setupDepartmentUsageTest(t)
	dept := mustDept(t, "戊部门")
	user := mustUserInDept(t, "usage-outside", &dept.Id)
	mustQuotaData(t, user, 50000, 7, 70, 700)

	_, resp := departmentRequest(t, http.MethodGet,
		"/api/data/departments?start_timestamp=1&end_timestamp=9999", "", nil, GetDepartmentUsage)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	rows := resp["data"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.EqualValues(t, 1, row["members"], "成员数不受时间区间影响")
	assert.EqualValues(t, 0, row["calls"])
}

func TestDepartmentUsageRejectsBadRange(t *testing.T) {
	setupDepartmentUsageTest(t)
	_, resp := departmentRequest(t, http.MethodGet, "/api/data/departments", "", nil, GetDepartmentUsage)
	assert.Equal(t, false, resp["success"])
}
