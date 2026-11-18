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
	return fmt.Sprintf(`{"id":%d,"role":%d,"department_id":%d}`, id, role, departmentID)
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
	body := fmt.Sprintf(`{"id":%d,"role":%d,"department_id":null}`, target.Id, common.RoleCommonUser)

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

func auditParamsOf(t *testing.T, entry model.AuditLog) map[string]any {
	t.Helper()
	require.NotNil(t, entry.Other.Op)
	encoded, err := common.Marshal(entry.Other.Op.Params)
	require.NoError(t, err)
	var params map[string]any
	require.NoError(t, common.Unmarshal(encoded, &params))
	return params
}

// 部门与角色分别按 user.department_update / user.manage 审计，
// 与单列端点和 ManageUser 的 action 口径一致。
func TestUnifiedUpdateAuditActions(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department", "role"},
		Role: common.RoleRootUser, DepartmentID: &dept.Id,
	})

	_, resp := unifiedUpdateRequest(t, unifiedUpdateBody(target.Id, common.RoleRootUser, dept.Id), proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var manageEntry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.manage").Order("id DESC").First(&manageEntry).Error)
	manageParams := auditParamsOf(t, manageEntry)
	assert.Equal(t, "grant_root", manageParams["action"])
	assert.Equal(t, float64(target.Id), manageParams["id"])
	assert.Equal(t, float64(common.RoleRootUser), manageParams["role"])
	assert.NotEmpty(t, manageParams["verification_method"])

	var departmentEntry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.department_update").Order("id DESC").First(&departmentEntry).Error)
	departmentParams := auditParamsOf(t, departmentEntry)
	assert.Equal(t, float64(target.Id), departmentParams["id"])
	assert.Equal(t, float64(dept.Id), departmentParams["department_id"])
}

// 部门改动不要求 step-up proof，与 PUT /api/user/:id/department 同口径。
func TestUnifiedUpdateDepartmentWithoutProof(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	body := fmt.Sprintf(`{"id":%d,"department_id":%d}`, target.Id, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, "", identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, dept.Id, *stored.DepartmentId)

	var entry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.department_update").Order("id DESC").First(&entry).Error)
	assert.Equal(t, float64(dept.Id), auditParamsOf(t, entry)["department_id"])
}

// 整对象回写的老客户端带上此前读到的旧部门，不得覆盖并发改过的归属。
func TestUnifiedUpdateWholeObjectWritebackKeepsCurrentDepartment(t *testing.T) {
	identity, target, staleDept := setupUnifiedUpdateTest(t)
	currentDept := model.Department{Name: "运营部"}
	require.NoError(t, model.DB.Save(&currentDept).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Update("department_id", currentDept.Id).Error)

	body := fmt.Sprintf(`{"id":%d,"username":"managed-user","display_name":"managed-user","group":"default","role":%d,"department_id":%d}`,
		target.Id, common.RoleCommonUser, staleDept.Id)
	_, resp := unifiedUpdateRequest(t, body, "", identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role)
	require.NotNil(t, stored.DepartmentId)
	assert.Equal(t, currentDept.Id, *stored.DepartmentId, "整对象回写中的旧快照不得改回部门")
	var audits int64
	require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Where("action = ?", "user.department_update").Count(&audits).Error)
	assert.Zero(t, audits, "被忽略的部门快照不应留下改归属审计")
}

// 资料列真正落库时必须写 user.update 审计，且只记列名不记值。
func TestUnifiedUpdateProfileChangeRecordsAudit(t *testing.T) {
	identity, target, _ := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"role"}, Role: common.RoleRootUser,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d,"display_name":"Audited Name","group":"vip"}`, target.Id, common.RoleRootUser)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var entry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.update").Order("id DESC").First(&entry).Error)
	params := auditParamsOf(t, entry)
	assert.Equal(t, float64(target.Id), params["id"])
	assert.ElementsMatch(t, []any{"display_name", "group"}, params["changed_fields"])
	auditJSON, err := common.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(auditJSON), "Audited Name")
}

// 合并路径撤销 root 必须与 ManageUser 同口径：user.manage/revoke_root 审计与 admin_demote 吊销原因。
func TestUnifiedUpdateRevokeRootAuditAndSessionReason(t *testing.T) {
	identity, target, _ := setupUnifiedUpdateTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Update("role", common.RoleRootUser).Error)
	now := time.Now().Unix()
	session := model.UserSession{
		SID: "unified-revoke-root-session", UserID: target.Id, Version: 1,
		UserAuthVersion: target.AuthVersion, Status: model.UserSessionStatusActive,
		RefreshHash: strings.Repeat("b", 64), LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}
	require.NoError(t, model.DB.Create(&session).Error)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"role"}, Role: common.RoleCommonUser,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d}`, target.Id, common.RoleCommonUser)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	var entry model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.manage").Order("id DESC").First(&entry).Error)
	assert.Equal(t, "revoke_root", auditParamsOf(t, entry)["action"])

	var stored model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", session.SID).First(&stored).Error)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
	assert.Equal(t, "admin_demote", stored.RevokedReason)
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

// 管理范围 proof 只绑定角色/部门。请求体夹带非空密码必须拒绝，
// 否则一份改部门的 proof 就能顺带重置目标账号密码，且审计看不出来。
func TestUnifiedUpdateRejectsPasswordWithManagementProof(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department"}, DepartmentID: &dept.Id,
	})
	body := fmt.Sprintf(`{"id":%d,"department_id":%d,"password":"fresh-password-9"}`, target.Id, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	assert.Equal(t, false, resp["success"], "message=%v", resp["message"])

	var row struct{ Password string }
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).
		Select("password").First(&row).Error)
	assert.Equal(t, target.Password, row.Password, "密码不得被管理范围 proof 顺带重置")

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Nil(t, stored.DepartmentId, "整次请求应被拒绝，部门也不落库")
}

// 角色 + 密码的组合同样必须拆分，管理范围 proof 里没有密码槽位。
func TestUnifiedUpdateRejectsPasswordWithRoleChangeProof(t *testing.T) {
	identity, target, _ := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"role"}, Role: common.RoleRootUser,
	})
	body := fmt.Sprintf(`{"id":%d,"role":%d,"password":"fresh-password-9"}`, target.Id, common.RoleRootUser)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	assert.Equal(t, false, resp["success"], "message=%v", resp["message"])

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.RoleCommonUser, stored.Role, "整次请求应被拒绝，角色也不落库")
}

// 权限矩阵虽已绑进 proof，但合并路径仍应拆到 admin.user.update。
func TestUnifiedUpdateRejectsPermissionMatrixWithManagementProof(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department"}, DepartmentID: &dept.Id,
		AdminPermissions: map[string]map[string]bool{"users": {"read": true}},
	})
	body := fmt.Sprintf(`{"id":%d,"department_id":%d,"admin_permissions":{"users":{"read":true}}}`, target.Id, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	assert.Equal(t, false, resp["success"], "message=%v", resp["message"])

	var rules int64
	require.NoError(t, model.DB.Model(&model.CasbinRule{}).
		Where("v0 = ?", authz.UserSubject(target.Id)).Count(&rules).Error)
	assert.Zero(t, rules, "权限矩阵不得搭管理范围 proof 的便车")

	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Nil(t, stored.DepartmentId, "整次请求应被拒绝，部门也不落库")
}

// 只传部分资料键时，缺失列不得用事务外读到的旧快照整份写回。
func TestUnifiedUpdatePartialProfileKeysDoNotRewriteOtherColumns(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Updates(map[string]any{
		"display_name": "Concurrent Name",
		"group":        "vip",
		"remark":       "concurrent note",
	}).Error)

	recorder := &updateSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	previous := model.DB
	model.DB = previous.Session(&gorm.Session{Logger: recorder})
	t.Cleanup(func() { model.DB = previous })

	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"role"}, Role: common.RoleRootUser,
	})
	body := fmt.Sprintf(`{"id":%d,"username":"managed-user","role":%d,"department_id":%d}`,
		target.Id, common.RoleRootUser, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	for _, sql := range recorder.sqls {
		if !strings.Contains(strings.ToUpper(sql), "UPDATE") {
			continue
		}
		assert.NotContains(t, sql, "display_name", "未传的资料列不得写入: %s", sql)
		assert.NotContains(t, sql, "remark", "未传的资料列不得写入: %s", sql)
	}
	stored, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	assert.Equal(t, "Concurrent Name", stored.DisplayName)
	assert.Equal(t, "vip", stored.Group)
	assert.Equal(t, "concurrent note", stored.Remark)
}

// password 键存在但值为空时，不得触发资料列整份写回。
func TestUnifiedUpdateEmptyPasswordDoesNotRewriteProfileColumns(t *testing.T) {
	identity, target, dept := setupUnifiedUpdateTest(t)
	recorder := &updateSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	previous := model.DB
	model.DB = previous.Session(&gorm.Session{Logger: recorder})
	t.Cleanup(func() { model.DB = previous })

	proof := unifiedUpdateProof(t, identity, service.AdminUserManagementContext{
		UserID: target.Id, Changes: []string{"department"}, DepartmentID: &dept.Id,
	})
	body := fmt.Sprintf(`{"id":%d,"department_id":%d,"password":""}`, target.Id, dept.Id)

	_, resp := unifiedUpdateRequest(t, body, proof, identity, common.RoleRootUser)
	require.Equal(t, true, resp["success"], "message=%v", resp["message"])

	for _, sql := range recorder.sqls {
		if !strings.Contains(strings.ToUpper(sql), "UPDATE") {
			continue
		}
		assert.NotContains(t, sql, "password", "空密码不得触发密码写入: %s", sql)
		assert.NotContains(t, sql, "display_name", "空密码不得触发资料列写回: %s", sql)
	}
}
