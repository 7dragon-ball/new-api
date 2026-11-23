package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDepartmentAssignTest(t *testing.T) (*model.User, model.Department) {
	t.Helper()
	_, _, target := setupAdminUserTest(t)
	require.NoError(t, model.DB.Exec("DELETE FROM department").Error)
	// UpdateUser 会刷新 authz 策略并 bump sync topic，测试环境需要备好这两处
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}))
	require.NoError(t, authz.Init(model.DB))
	dept := model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(&dept).Error)
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

// 缺字段与显式 null 在 *int 解码后都是 nil，都会被当成「清空」。
// 本接口只做改部门一件事，没办法从请求形状上区分「客户端忘了传」与「有意清空」，
// 所以字段缺失必须报错 —— 否则 BFF 参数名拼错就会静默把部门清空。
func TestUpdateUserDepartmentRejectsMissingField(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)

	for _, body := range []string{`{}`, `{"department":1}`, `{"department_id_typo":1}`} {
		_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
			body, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
		assert.Equal(t, false, resp["success"], "body=%s 必须报错而不是清空部门", body)
		assert.Equal(t, "Invalid parameters", resp["message"], "body=%s", body)

		stored, err := model.GetUserById(target.Id, false)
		require.NoError(t, err)
		require.NotNil(t, stored.DepartmentId, "body=%s 被拒时部门不得被清空", body)
		assert.Equal(t, dept.Id, *stored.DepartmentId, "body=%s", body)
	}
}

// 显式 null 才是清空；类型不符、0、负数、小数都必须被拒且不改动原值。
func TestUpdateUserDepartmentRejectsMalformedValue(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)

	for _, body := range []string{`{"department_id":"1"}`, `{"department_id":0}`, `{"department_id":-3}`, `{"department_id":1.5}`} {
		_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
			body, gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
		assert.Equal(t, false, resp["success"], "body=%s", body)

		stored, err := model.GetUserById(target.Id, false)
		require.NoError(t, err)
		require.NotNil(t, stored.DepartmentId, "body=%s 被拒时部门不得变化", body)
		assert.Equal(t, dept.Id, *stored.DepartmentId, "body=%s", body)
	}
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

func TestUpdateUserDepartmentAuditContentRendersValue(t *testing.T) {
	// 审计 Content 的模板是一条给人看的英文句子，占位符用 %v 渲染。
	// 传 *int 会把指针地址写进去（"to 0xc0004a21b0"），清空时写 "<nil>"。
	target, dept := setupDepartmentAssignTest(t)
	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":`+strconv.Itoa(dept.Id)+`}`,
		gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"])

	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.department_update").Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Content, "to "+strconv.Itoa(dept.Id))
	assert.NotContains(t, logs[0].Content, "0x", "Content 里不得出现指针地址")
}

// departmentRequestAs 与 departmentRequest 相同，但可以指定调用方角色。
// 同目录其它管理写路径（UpdateUser / ManageUser / DeleteUser）都用 canManageTargetRole
// 挡「同级或更高」的目标，这里要能构造出低角色管理员来验证同一条边界。
func departmentRequestAs(t *testing.T, role int, method, path, body string, params gin.Params, handler gin.HandlerFunc) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", 1)
	c.Set("username", "admin")
	c.Set("role", role)
	handler(c)

	var decoded map[string]any
	if response.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &decoded), "body=%s", response.Body.String())
	}
	return response, decoded
}

func TestUpdateUserDepartmentRejectsHigherRoleTarget(t *testing.T) {
	// role=10 的管理员改 role=100 的账号：连 GET /api/user/:id 都会被 canManageTargetRole 拒，
	// 改部门也必须拒，否则授权分层被绕过（虽然 role=10 账号只能 out-of-band 建出来）。
	target, dept := setupDepartmentAssignTest(t)
	require.NoError(t, model.DB.Model(target).Update("role", common.RoleRootUser).Error)
	before := model.Department{Name: "原部门"}
	require.NoError(t, model.DB.Save(&before).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", before.Id).Error)

	_, resp := departmentRequestAs(t, common.RoleAdminUser,
		http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":`+strconv.Itoa(dept.Id)+`}`,
		gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)

	assert.Equal(t, false, resp["success"])
	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, before.Id, *stored.DepartmentId, "被拒时部门不得变化")
}

func TestUpdateUserDepartmentAllowsLowerRoleTarget(t *testing.T) {
	// role=10 改 role=1 是允许的（canManageTargetRole(10,1) 为真），这条钉住修复不会把正常路径拦掉。
	target, dept := setupDepartmentAssignTest(t)
	_, resp := departmentRequestAs(t, common.RoleAdminUser,
		http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":`+strconv.Itoa(dept.Id)+`}`,
		gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
}

// 回归：改归属成功后必须刷新鉴权缓存。部门改动不动 auth_version，缓存不会自己失效，
// 不刷新就有最多一个 TTL 的旧部门名；这期间 WriteContext 写出的日志与 quota_data
// 快照会永久落在旧部门上（quota_data 的聚合键含 department，还会把同一用户拆成两行）。
func TestUpdateUserDepartmentRefreshesAuthCache(t *testing.T) {
	target, dept := setupDepartmentAssignTest(t)
	server := miniredis.RunT(t)
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = previousRedisEnabled, previousRDB
	})
	// 先灌一份「改归属前」的缓存：此时用户没有部门。
	cached, err := model.GetUserCache(target.Id)
	require.NoError(t, err)
	require.Empty(t, cached.DepartmentName)
	require.NotEmpty(t, server.Keys(), "前提：缓存确实命中 Redis，否则本用例测不到陈旧快照")

	_, resp := departmentRequest(t, http.MethodPut, "/api/user/"+strconv.Itoa(target.Id)+"/department",
		`{"department_id":`+strconv.Itoa(dept.Id)+`}`,
		gin.Params{{Key: "id", Value: strconv.Itoa(target.Id)}}, UpdateUserDepartment)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	cached, err = model.GetUserCache(target.Id)
	require.NoError(t, err)
	assert.Equal(t, "技术部", cached.DepartmentName, "改归属后缓存里必须是新部门，否则后续日志快照会记成旧部门")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	cached.WriteContext(c)
	assert.Equal(t, "技术部", common.GetContextKeyString(c, constant.ContextKeyUserDepartmentName),
		"写日志/用量的身份快照直接读上下文，这里必须是新部门")

	// 快照链路端到端：紧接着写出的日志行必须是新部门。
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	previousLogConsume := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsume })
	model.RecordConsumeLog(c, target.Id, model.RecordConsumeLogParams{
		ModelName: "dept-snapshot", Quota: 1, Group: "default", Other: model.NewLogOther(),
	})
	var written model.Log
	require.NoError(t, model.LOG_DB.Order("id desc").First(&written).Error)
	assert.Equal(t, "技术部", written.Department, "日志快照一旦写入无法回填，必须是新部门")
}
