package controller

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDepartmentAssignTest(t *testing.T) (*model.User, model.Department) {
	t.Helper()
	_, identity, target := setupAdminUserTest(t)
	require.NoError(t, model.DB.Exec("DELETE FROM department").Error)
	// UpdateUser 会刷新 authz 策略并 bump sync topic，测试环境需要备好这两处
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}))
	require.NoError(t, authz.Init(model.DB))
	dept := model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(&dept).Error)
	_ = identity
	return target, dept
}

func TestUpdateUserDepartmentAssigns(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	body := `{"department_id":` + strconv.Itoa(dept.Id) + `}`
	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		body, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)
}

func TestUpdateUserDepartmentClearsWithNull(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)

	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":null}`, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Nil(t, stored.DepartmentId, "null 必须真的写成 NULL，不能被 GORM 跳过零值")
}

func TestUpdateUserDepartmentRejectsUnknownDepartment(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)

	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":999999}`, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	assert.Equal(t, false, resp["success"])
	assert.Equal(t, "Department not found", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId, "被拒时原值不得变化")
}

func TestUpdateUserDepartmentRejectsUnknownUser(t *testing.T) {
	setupDepartmentAssignTest(t)
	_, resp := departmentRequest(t, http.MethodPut, "/api/user/999999/department",
		`{"department_id":null}`, gin.Params{{Key: "id", Value: "999999"}}, UpdateUserDepartment)
	assert.Equal(t, false, resp["success"])
	assert.Equal(t, "User not found", resp["message"])
}

func TestUpdateUserDepartmentRejectsBadId(t *testing.T) {
	setupDepartmentAssignTest(t)
	for _, raw := range []string{"abc", "0", "-1"} {
		_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+raw+"/department",
			`{"department_id":null}`, gin.Params{{Key: "id", Value: raw}}, UpdateUserDepartment)
		assert.Equal(t, false, resp["success"], "id=%s", raw)
	}
}

func TestUpdateUserEndpointDoesNotTouchDepartment(t *testing.T) {
	// 老的整行替换接口（上游自带前端会调）不带 department_id 时，
	// 部门必须原样保留 —— 这是「不复用 PUT /api/user/」的核心原因。
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)

	body := `{"id":` + strconv.Itoa(target.Id) + `,"username":"managed-user","display_name":"改名后","remark":"x"}`
	_, resp := departmentRequest(t, http.MethodPut, "/api/user/", body, nil, UpdateUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId, "整行替换不得把部门清空")
	assert.Equal(t, dept.Id, *stored.DepartmentId)
	assert.Equal(t, "改名后", stored.DisplayName, "其余字段仍应正常写入")
}

func TestUpdateUserDepartmentValueInAuditParams(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	body := `{"department_id":` + strconv.Itoa(dept.Id) + `}`
	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		body, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"])

	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Where("action = ?", "user.department_update").Count(&count).Error)
	assert.EqualValues(t, 1, count, "每次改归属恰好一条审计（markAuditLogged 生效则不会翻倍）")
}

var _ = common.RoleRootUser
