package controller

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUserSearchControllerTest(t *testing.T) (*model.User, service.AuthIdentity) {
	t.Helper()
	operator, identity, _ := setupAdminUserTest(t)
	require.NoError(t, model.DB.Exec("DELETE FROM department").Error)
	// CreateUser 建普通用户会顺带清一次用户权限并刷新 authz 策略，
	// 测试环境需要把这两张表和 enforcer 准备好，否则接口回 500。
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}))
	require.NoError(t, authz.Init(model.DB))
	return operator, identity
}

func TestSearchUsersRejectsInvalidDepartmentId(t *testing.T) {
	setupUserSearchControllerTest(t)
	for _, raw := range []string{"abc", "0", "-3"} {
		response, resp := departmentRequest(t, http.MethodGet,
			"/api/user/search?department_id="+raw, "", nil, SearchUsers)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, false, resp["success"], "department_id=%s 必须显式报错而不是静默忽略", raw)
		assert.Equal(t, "Invalid parameters", resp["message"], "department_id=%s", raw)
	}
}

func TestSearchUsersUnknownDepartmentReturnsEmptyPage(t *testing.T) {
	setupUserSearchControllerTest(t)
	response, resp := departmentRequest(t, http.MethodGet,
		"/api/user/search?department_id=999999", "", nil, SearchUsers)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, true, resp["success"], "message=%v", resp["message"])
	data := resp["data"].(map[string]any)
	assert.EqualValues(t, 0, data["total"])
	assert.Empty(t, data["items"])
}

func TestSearchUsersUsernameFilterIsExact(t *testing.T) {
	setupUserSearchControllerTest(t)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "bob", Password: "password123", DisplayName: "Bob",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "bob-aff",
	}).Error)

	_, resp := departmentRequest(t, http.MethodGet,
		"/api/user/search?username=managed-user", "", nil, SearchUsers)
	assert.Equal(t, true, resp["success"], "message=%v", resp["message"])
	items := resp["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "managed-user", items[0].(map[string]any)["username"])
}

func TestCreateUserReturnsIdAndStoresDepartment(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	dept := &model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(dept).Error)

	// role < 10 不触发 step-up，无需 proof
	body := `{"username":"dept-create","password":"password-123","department_id":` + strconv.Itoa(dept.Id) + `}`
	response := adminUserRequest(http.MethodPost, "/api/user/", body, "", identity, common.RoleRootUser, nil, CreateUser)

	var result struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, "message=%s", result.Message)

	newId, ok := result.Data["id"].(float64)
	require.True(t, ok, "响应必须带 data.id，实际 data=%v", result.Data)
	require.Greater(t, newId, float64(0))

	stored, err := model.GetUserById(int(newId), false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)
}

func TestCreateUserWithoutDepartmentStillReturnsId(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	body := `{"username":"plain-create","password":"password-123"}`
	response := adminUserRequest(http.MethodPost, "/api/user/", body, "", identity, common.RoleRootUser, nil, CreateUser)

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Id float64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, "message=%s", result.Message)
	require.Greater(t, result.Data.Id, float64(0))

	stored, err := model.GetUserById(int(result.Data.Id), false)
	require.NoError(t, err)
	assert.Nil(t, stored.DepartmentId)
}

func TestCreateUserRejectsUnknownDepartmentWithoutInsertingRow(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	var before int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&before).Error)

	body := `{"username":"ghost-dept","password":"password-123","department_id":999999}`
	response := adminUserRequest(http.MethodPost, "/api/user/", body, "", identity, common.RoleRootUser, nil, CreateUser)

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Equal(t, "Department not found", result.Message)

	var after int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&after).Error)
	assert.EqualValues(t, before, after, "被拒时 users 表不得新增行")
}
