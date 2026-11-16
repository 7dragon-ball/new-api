package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// departmentRequest 驱动部门 handler，带上 AdminAuth 会填好的上下文。
func departmentRequest(t *testing.T, method, path, body string, params gin.Params, handler gin.HandlerFunc) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", 1)
	c.Set("username", "admin")
	c.Set("role", common.RoleRootUser)
	handler(c)

	var decoded map[string]any
	if response.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &decoded), "body=%s", response.Body.String())
	}
	return response, decoded
}

func setupDepartmentControllerTest(t *testing.T) {
	t.Helper()
	_, _ = setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Exec("DELETE FROM department").Error)
}

func TestCreateDepartmentNormalizesAndRejectsDuplicate(t *testing.T) {
	setupDepartmentControllerTest(t)

	_, created := departmentRequest(t, http.MethodPost, "/api/department/",
		`{"name":"  技术部  "}`, nil, CreateDepartment)
	require.Equal(t, true, created["success"], "message=%v", created["message"])
	data := created["data"].(map[string]any)
	assert.Equal(t, "技术部", data["name"], "落库前必须归一")

	_, duplicate := departmentRequest(t, http.MethodPost, "/api/department/",
		`{"name":"技术部"}`, nil, CreateDepartment)
	assert.Equal(t, false, duplicate["success"])
	assert.Equal(t, "Department name already exists", duplicate["message"])

	var count int64
	require.NoError(t, model.DB.Model(&model.Department{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "同名再建不得新增行")
}

func TestCreateDepartmentRejectsInvalidName(t *testing.T) {
	setupDepartmentControllerTest(t)
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("部", 31) + `"}`} {
		_, resp := departmentRequest(t, http.MethodPost, "/api/department/", body, nil, CreateDepartment)
		assert.Equal(t, false, resp["success"], "body=%s", body)
		assert.Equal(t, "Department name must be 1-30 characters (spaces are trimmed)", resp["message"], "body=%s", body)
	}
}

func TestUpdateDepartmentUsesSaveSoNormalizationApplies(t *testing.T) {
	setupDepartmentControllerTest(t)
	dept := &model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(dept).Error)

	_, resp := departmentRequest(t, http.MethodPut, "/api/department/",
		`{"id":`+strconv.Itoa(dept.Id)+`,"name":" 研发部 "}`, nil, UpdateDepartment)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	assert.Equal(t, "研发部", resp["data"].(map[string]any)["name"])

	var stored model.Department
	require.NoError(t, model.DB.First(&stored, dept.Id).Error)
	assert.Equal(t, "研发部", stored.Name, "库里存的必须是归一后的名字")
}

func TestUpdateDepartmentRejectsMissingAndDuplicate(t *testing.T) {
	setupDepartmentControllerTest(t)
	first := &model.Department{Name: "技术部"}
	second := &model.Department{Name: "产品部"}
	require.NoError(t, model.DB.Save(first).Error)
	require.NoError(t, model.DB.Save(second).Error)

	_, notFound := departmentRequest(t, http.MethodPut, "/api/department/",
		`{"id":999999,"name":"x"}`, nil, UpdateDepartment)
	assert.Equal(t, false, notFound["success"])

	// 归一同名：改名去撞另一个部门（带空格变体）也要被挡住
	_, duplicate := departmentRequest(t, http.MethodPut, "/api/department/",
		`{"id":`+strconv.Itoa(second.Id)+`,"name":" 技术部 "}`, nil, UpdateDepartment)
	assert.Equal(t, false, duplicate["success"])
	assert.Equal(t, "Department name already exists", duplicate["message"])
}

func TestDeleteDepartmentRejectsWhenActiveUsersExist(t *testing.T) {
	setupDepartmentControllerTest(t)
	dept := &model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(dept).Error)
	user := &model.User{Username: "dept-worker", Password: "password123", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "dept-worker-aff", DepartmentId: &dept.Id}
	require.NoError(t, model.DB.Create(user).Error)

	_, resp := departmentRequest(t, http.MethodDelete, "/api/department/"+strconv.Itoa(dept.Id),
		"", gin.Params{{Key: "id", Value: strconv.Itoa(dept.Id)}}, DeleteDepartment)
	assert.Equal(t, false, resp["success"])
	assert.Contains(t, resp["message"], "1 users", "文案里要带人数")

	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId, "被拒时用户的部门归属不得变化")
}

func TestDeleteDepartmentSucceedsWhenOnlySoftDeletedUsersExist(t *testing.T) {
	setupDepartmentControllerTest(t)
	dept := &model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(dept).Error)
	user := &model.User{Username: "dept-gone", Password: "password123", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "dept-gone-aff", DepartmentId: &dept.Id}
	require.NoError(t, model.DB.Create(user).Error)
	// 软删：物理行仍在，计数必须看不见它 —— 这正是不能用外键 RESTRICT 的原因
	require.NoError(t, model.DB.Delete(&model.User{}, user.Id).Error)

	_, resp := departmentRequest(t, http.MethodDelete, "/api/department/"+strconv.Itoa(dept.Id),
		"", gin.Params{{Key: "id", Value: strconv.Itoa(dept.Id)}}, DeleteDepartment)
	assert.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var count int64
	require.NoError(t, model.DB.Model(&model.Department{}).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}

func TestDeleteDepartmentSucceedsWhenEmpty(t *testing.T) {
	setupDepartmentControllerTest(t)
	dept := &model.Department{Name: "空部门"}
	require.NoError(t, model.DB.Save(dept).Error)

	_, resp := departmentRequest(t, http.MethodDelete, "/api/department/"+strconv.Itoa(dept.Id),
		"", gin.Params{{Key: "id", Value: strconv.Itoa(dept.Id)}}, DeleteDepartment)
	assert.Equal(t, true, resp["success"], "message=%v", resp["message"])
}

func TestDeleteDepartmentRejectsUnknownId(t *testing.T) {
	setupDepartmentControllerTest(t)
	_, resp := departmentRequest(t, http.MethodDelete, "/api/department/999999",
		"", gin.Params{{Key: "id", Value: "999999"}}, DeleteDepartment)
	assert.Equal(t, false, resp["success"])
}

func TestGetDepartmentsFiltersByKeywordInsideName(t *testing.T) {
	setupDepartmentControllerTest(t)
	for _, name := range []string{"技术部", "产品部", "技术支持部"} {
		require.NoError(t, model.DB.Save(&model.Department{Name: name}).Error)
	}

	response, resp := departmentRequest(t, http.MethodGet, "/api/department/?keyword="+urlEscape("技术"),
		"", nil, GetDepartments)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	_ = response

	rows, ok := resp["data"].([]any)
	require.True(t, ok, "data 必须是数组（前端两处调用依赖这个形状），实际=%T", resp["data"])
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.(map[string]any)["name"].(string))
	}
	assert.ElementsMatch(t, []string{"技术部", "技术支持部"}, names, "必须含中间匹配")
}

func TestGetDepartmentsKeywordWithWildcardIsLiteral(t *testing.T) {
	setupDepartmentControllerTest(t)
	require.NoError(t, model.DB.Save(&model.Department{Name: "100%达成"}).Error)
	require.NoError(t, model.DB.Save(&model.Department{Name: "技术部"}).Error)

	// 用 LIKE 时 "%" 会命中全部；position 才把它当字面量
	_, resp := departmentRequest(t, http.MethodGet, "/api/department/?keyword="+urlEscape("%"),
		"", nil, GetDepartments)
	rows := resp["data"].([]any)
	assert.Len(t, rows, 1, "keyword=%% 只能命中名字里真含 %% 的那条")
}

func urlEscape(raw string) string {
	return url.QueryEscape(raw)
}
