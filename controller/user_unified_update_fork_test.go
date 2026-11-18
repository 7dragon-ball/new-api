package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// [FORK] 合并编辑用户：PUT /api/user/ 一次请求同时写角色与部门。
// 这些用例关注两件事：改动落在同一个事务里，以及 proof 绑定了本次要改的全部字段。

func setupUnifiedUpdateTest(t *testing.T) (service.AuthIdentity, *model.User, model.Department) {
	t.Helper()
	_, identity, target := setupAdminUserTest(t)
	require.NoError(t, model.DB.Exec("DELETE FROM department").Error)
	// 降级会清 Casbin override，authzTouched 又会 bump sync topic，两处都要备好。
	require.NoError(t, model.DB.AutoMigrate(&model.SystemTask{}))
	require.NoError(t, authz.Init(model.DB))
	dept := model.Department{Name: "技术部"}
	require.NoError(t, model.DB.Save(&dept).Error)
	return identity, target, dept
}

func unifiedUpdateProof(t *testing.T, identity service.AuthIdentity, ctx service.AdminUserManagementContext) string {
	t.Helper()
	payload, err := common.Marshal(ctx)
	require.NoError(t, err)
	return issueSecurityEnrollmentProof(t, identity,
		service.VerificationOperation{Scope: service.VerificationScopeAdminUserManagement, Context: payload},
		service.VerificationMethodPassword)
}

func unifiedUpdateRequest(t *testing.T, body, proof string, identity service.AuthIdentity, role int) (int, map[string]any) {
	t.Helper()
	recorder := adminUserRequest(http.MethodPut, "/api/user/", body, proof, identity, role, nil, UpdateUser)
	var resp map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp), "body=%s", recorder.Body.String())
	return recorder.Code, resp
}

func unifiedUpdateBody(id, role, departmentID int) string {
	return fmt.Sprintf(`{"id":%d,"username":"managed-user","display_name":"managed-user","group":"default","role":%d,"department_id":%d}`, id, role, departmentID)
}

func TestUnifiedUpdateAppliesRoleAndDepartmentTogether(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &dept.Id,
	})

	_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleRootUser, dept.Id), proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleRootUser, stored.Role)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)
	assert.Greater(t, stored.AuthVersion, target.AuthVersion, "改角色必须推进 auth_version")
}

// 部门校验在事务最开头失败：角色列也没机会写入，这是「不会半成功」的核心保证。
func TestUnifiedUpdateRollsBackRoleWhenDepartmentIsMissing(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)
	missing := 999999
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &missing,
	})

	_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleRootUser, missing), proof, identity, common.RoleRootUser)
	assert.Equal(t, false, resp["success"])
	assert.Equal(t, "Department not found", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role, "事务回滚后角色不得变化")
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId, "事务回滚后部门不得变化")
	assert.Equal(t, target.AuthVersion, stored.AuthVersion, "auth_version 也不得推进")
}

// 降级与清空部门同事务成功：nil 必须真的落成 NULL，不能被 GORM 跳过零值。
func TestUnifiedUpdateDemotesAndClearsDepartment(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Updates(map[string]any{"role": common.RoleRootUser, "department_id": dept.Id}).Error)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleCommonUser,
	})
	body := fmt.Sprintf(`{"id":%d,"username":"managed-user","display_name":"managed-user","group":"default","role":%d,"department_id":null}`,
		target.Id, common.RoleCommonUser)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role)
	assert.Nil(t, stored.DepartmentId)
}

// role 等于库中当前值时按 no-op 处理，兼容整行回写的旧客户端：只改部门、只验部门。
func TestUnifiedUpdateRoleNoopOnlyChangesDepartment(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department"}, DepartmentID: &dept.Id,
	})

	_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleCommonUser, dept.Id), proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)
}

func TestUnifiedUpdateRejectsProofBoundToDifferentDepartment(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	other := dept.Id + 1000
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &other,
	})

	code, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleRootUser, dept.Id), proof, identity, common.RoleRootUser)
	assert.Equal(t, http.StatusForbidden, code, "body=%v", resp)

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role)
	assert.Nil(t, stored.DepartmentId, "proof 不匹配时两列都不能写")
}

func TestUnifiedUpdateRejectsInvalidRoleBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     int
		operator int
	}{
		{"role outside whitelist", common.RoleAdminUser, common.RoleRootUser},
		{"non-root grants root", common.RoleRootUser, common.RoleAdminUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, target, dept := setupUnifiedUpdateTest(t)
			_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, tc.role, dept.Id), "", identity, tc.operator)
			assert.Equal(t, false, resp["success"])

			stored, err := model.GetUserById(target.Id, false)
			require.NoError(t, err)
			assert.Equal(t, common.RoleCommonUser, stored.Role)
			assert.Nil(t, stored.DepartmentId)
		})
	}
}

func TestUnifiedUpdateRejectsEmptyPermissionMatrix(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	body := fmt.Sprintf(`{"id":%d,"username":"managed-user","display_name":"managed-user","group":"default","role":%d,"department_id":%d,"admin_permissions":{}}`,
		target.Id, common.RoleRootUser, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, "", identity, common.RoleRootUser)
	assert.Equal(t, false, resp["success"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role)
	assert.Nil(t, stored.DepartmentId)
}

// 只带 id/role/department_id 的调用（BFF 常见）不能把没重发的资料列清成零值。
func TestUnifiedUpdateKeepsUnspecifiedProfileFields(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Updates(map[string]any{
		"display_name": "Original Name",
		"group":        "vip",
		"remark":       "keep me",
	}).Error)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &dept.Id,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d,"department_id":%d}`, target.Id, common.RoleRootUser, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, "Original Name", stored.DisplayName)
	assert.Equal(t, "vip", stored.Group)
	assert.Equal(t, "keep me", stored.Remark)
	assert.Equal(t, common.RoleRootUser, stored.Role)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)
	assert.Greater(t, stored.AuthVersion, target.AuthVersion, "改角色必须推进 auth_version")
}

// 分流条件必须允许「只改一个」：两个键同时存在才接管的写法会让单边编辑失效。
func TestUnifiedUpdateSingleKeyDispatch(t *testing.T) {
	t.Run("department only", func(t *testing.T) {
		identity, target, dept := setupUnifiedUpdateTest(t)
		proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
			UserID: target.Id, Changes: []string{"department"}, DepartmentID: &dept.Id,
		})
		body := fmt.Sprintf(`{"id":%d,"department_id":%d}`, target.Id, dept.Id)

		_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
		require.Equal(t, true, resp["success"], "message=%v", resp["message"])
		stored, err := model.GetUserById(target.Id, false)
		require.NoError(t, err)
		require.NotNil(t, stored.DepartmentId)
		assert.Equal(t, dept.Id, *stored.DepartmentId)
		assert.Equal(t, common.RoleCommonUser, stored.Role)
	})
	t.Run("role only", func(t *testing.T) {
		identity, target, dept := setupUnifiedUpdateTest(t)
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Update("department_id", dept.Id).Error)
		proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
			UserID: target.Id, Changes: []string{"role"}, Role: common.RoleRootUser,
		})
		body := fmt.Sprintf(`{"id":%d,"role":%d}`, target.Id, common.RoleRootUser)

		_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
		require.Equal(t, true, resp["success"], "message=%v", resp["message"])
		stored, err := model.GetUserById(target.Id, false)
		require.NoError(t, err)
		assert.Equal(t, common.RoleRootUser, stored.Role)
		require.NotNil(t, stored.DepartmentId)
		assert.Equal(t, dept.Id, *stored.DepartmentId, "没传 department_id 时部门不得被动")
	})
}

// 前端审计明细只识别 changed_fields；目标值也要留痕，否则看不出改成了什么。
func TestUnifiedUpdateAuditRecordsChangedFields(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &dept.Id,
	})

	_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleRootUser, dept.Id), proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var entry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.update").Order("id DESC").First(&entry).Error)
	require.NotNil(t, entry.Other.Op)
	encoded, err := common.Marshal(entry.Other.Op.Params)
	require.NoError(t, err)
	var params map[string]any
	require.NoError(t, common.Unmarshal(encoded, &params))
	assert.Equal(t, []any{"department", "role"}, params["changed_fields"])
	assert.Equal(t, float64(common.RoleRootUser), params["role"])
	assert.Equal(t, float64(dept.Id), params["department_id"])
}

// 部分字段请求会跳过 EditWithTx，改角色后仍必须读回新的 auth_version，
// 否则会话吊销这一步会被漏掉。
func TestUnifiedUpdatePartialBodyRevokesSessions(t *testing.T) {
	identity, target, _ := setupUnifiedUpdateTest(t)
	now := time.Now().Unix()
	session := model.UserSession{
		SID:             "unified-update-target-session",
		UserID:          target.Id,
		Version:         1,
		UserAuthVersion: target.AuthVersion,
		Status:          model.UserSessionStatusActive,
		RefreshHash:     strings.Repeat("a", 64),
		LoginMethod:     "password",
		LastActiveAt:    now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(&session).Error)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"role"}, Role: common.RoleRootUser,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d}`, target.Id, common.RoleRootUser)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var stored model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", session.SID).First(&stored).Error)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
	assert.Equal(t, "admin_user_update", stored.RevokedReason)
}

type updateSQLRecorder struct {
	logger.Interface
	sqls []string
}

func (r *updateSQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.sqls = append(r.sqls, sql)
}

// 没回写资料时即使补齐了快照也不整份写回：否则会静默覆盖并发发生的资料修改。
// 结果值相同，只能从实际执行的 SQL 上钉住这条边界。
func TestUnifiedUpdatePartialBodyDoesNotWriteProfileColumns(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	recorder := &updateSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	previous := model.DB
	model.DB = previous.Session(&gorm.Session{Logger: recorder})
	t.Cleanup(func() { model.DB = previous })

	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &dept.Id,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d,"department_id":%d}`, target.Id, common.RoleRootUser, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])
	for _, sql := range recorder.sqls {
		if strings.Contains(strings.ToUpper(sql), "UPDATE") {
			assert.NotContains(t, sql, "display_name", "部分字段请求不得写资料列: %s", sql)
		}
	}
}
