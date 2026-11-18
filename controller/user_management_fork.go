package controller

import (
	"bytes"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// [FORK] 合并编辑用户：PUT /api/user/ 一次请求同时写角色与部门。
//
// 上游 UpdateUser 只在开头加了一个分派，函数体保持原样。本文件接管 body 里
// 带 role 或 department_id 键的请求；其余请求（含现有的纯资料编辑）一律
// 交回上游。因此这里的所有行为差异都只影响新增的合并编辑路径。
//
// 语义是「传了哪个键就改哪个」：department_id 显式传 null 表示清空部门，
// role 为零值或与库中相同表示不改角色（兼容整行回写的旧客户端）。
// 整对象回写（带 username 等资料列）里的 department_id 是调用方读到的旧快照，
// 不视为改部门；显式改归属请走 PUT /api/user/:id/department 或只发
// id/role/department_id。部门不要求 step-up proof，审计同为 user.department_update；
// 只有真正改角色才要求 admin.user.management proof。

// handleUserManagementUpdate 在接管时返回 true（已写完响应），否则返回 false
// 让上游继续。分流要读 body 判键，而上游用 common.DecodeJson 读一次即尽，
// 所以不接管时也要把 body 还原回去，否则老请求会解析成空对象。
func handleUserManagementUpdate(c *gin.Context) bool {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil {
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	var keys map[string]common.RawMessage
	if common.Unmarshal(body, &keys) != nil {
		return false
	}
	_, hasRole := keys["role"]
	_, hasDepartment := keys["department_id"]
	if !hasRole && !hasDepartment {
		return false
	}

	var updatedUser model.User
	if common.Unmarshal(body, &updatedUser) != nil || updatedUser.Id == 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return true
	}
	updateUserManagement(c, &updatedUser, keys)
	return true
}

func updateUserManagement(c *gin.Context, updatedUser *model.User, keys map[string]common.RawMessage) {
	_, hasRole := keys["role"]
	departmentRaw, hasDepartment := keys["department_id"]
	// 整对象回写会带上调用方此前读到的 department_id；它落后于并发改归属时，
	// 写回会把部门静默改脏且不留审计。这类请求只按资料列与角色处理，部门一律忽略。
	if _, wholeObject := keys["username"]; hasDepartment && wholeObject {
		hasDepartment = false
	}

	originUser, err := model.GetUserById(updatedUser.Id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// 部分字段请求（BFF 常见）只发 id/role/department_id。用库中当前值补齐
	// 资料列仅供下面的校验使用；实际落库只写 keys 里真正出现的列。
	if _, ok := keys["username"]; !ok {
		updatedUser.Username = originUser.Username
	}
	if _, ok := keys["display_name"]; !ok {
		updatedUser.DisplayName = originUser.DisplayName
	}
	if _, ok := keys["group"]; !ok {
		updatedUser.Group = originUser.Group
	}
	if _, ok := keys["remark"]; !ok {
		updatedUser.Remark = originUser.Remark
	}
	updatedUser.Username = strings.TrimSpace(updatedUser.Username)
	if updatedUser.Username == "" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := common.Validate.StructExcept(updatedUser, "Password"); err != nil {
		common.ApiErrorT(c, "Invalid input {{error}}", map[string]any{"error": err.Error()})
		return
	}
	myRole := c.GetInt("role")
	if !canManageTargetRole(myRole, originUser.Role) {
		common.ApiErrorT(c, "No permission to update users of same or higher permission level")
		return
	}
	// 部门：显式 null 清空，正整数设置；键不存在则完全不动。
	var departmentID *int
	if hasDepartment && common.GetJsonType(departmentRaw) != "null" {
		var parsed int
		if common.Unmarshal(departmentRaw, &parsed) != nil || parsed <= 0 {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		departmentID = &parsed
	}
	roleChanged := hasRole && updatedUser.Role != 0 && updatedUser.Role != originUser.Role
	if roleChanged {
		// 只开放普通用户与超级管理员；提为 root 只能由 root 操作。
		if updatedUser.Role != common.RoleCommonUser && updatedUser.Role != common.RoleRootUser {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		if updatedUser.Role == common.RoleRootUser && myRole != common.RoleRootUser {
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
			return
		}
	}
	// 写入后的有效角色，决定权限矩阵按哪个角色落库。
	effectiveRole := originUser.Role
	if roleChanged {
		effectiveRole = updatedUser.Role
	}
	// 空矩阵在 proof context 里与「没传」同形，审批提示无法表达「清空全部权限」。
	// 合并编辑时直接拒绝，请分开提交。
	if updatedUser.AdminPermissions != nil && len(updatedUser.AdminPermissions) == 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}

	updatePassword := updatedUser.Password != ""
	// 只写请求里显式出现的资料列；空密码不算改动，补齐的快照值不参与落库。
	profileUpdates := map[string]any{}
	if _, ok := keys["username"]; ok {
		profileUpdates["username"] = updatedUser.Username
	}
	if _, ok := keys["display_name"]; ok {
		profileUpdates["display_name"] = updatedUser.DisplayName
	}
	if _, ok := keys["group"]; ok {
		profileUpdates["group"] = updatedUser.Group
	}
	if _, ok := keys["remark"]; ok {
		profileUpdates["remark"] = updatedUser.Remark
	}
	if updatePassword {
		profileUpdates["password"] = updatedUser.Password
	}
	auditParams := map[string]any{
		"username": originUser.Username,
		"id":       updatedUser.Id,
	}
	// 一次 proof 绑住本次要改的角色与部门目标值；绑不了「清空全部权限」，
	// 所以上面先拒绝了空矩阵。
	changes := make([]string, 0, 2)
	if roleChanged {
		changes = append(changes, "role")
	}
	if hasDepartment {
		changes = append(changes, "department")
	}
	slices.Sort(changes)
	// 管理范围 proof 只绑角色/部门；密码与权限矩阵必须拆到 admin.user.update，
	// 否则一份改角色的 proof 就能顺带重置凭据，且审计不会留下痕迹。
	if roleChanged && (updatePassword || updatedUser.AdminPermissions != nil) {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	ctx := service.AdminUserManagementContext{
		UserID:           updatedUser.Id,
		Changes:          changes,
		DepartmentID:     departmentID,
		AdminPermissions: updatedUser.AdminPermissions,
	}
	if roleChanged {
		ctx.Role = updatedUser.Role
	}
	var verificationMethod string
	if roleChanged {
		// 部门与单列端点同口径，不要求 proof；只有真正改角色才 step-up。
		authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserManagement, ctx)
		if authorization == nil {
			return
		}
		verificationMethod = authorization.Method
	} else if updatePassword || updatedUser.AdminPermissions != nil {
		// role 键为 no-op 或只改部门时夹带凭据：与上游一样走 admin.user.update。
		// 整对象回写的老客户端带上未变的 role/department_id，不会被新 scope 拦下。
		authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserUpdate, service.AdminUserContext{UserID: updatedUser.Id})
		if authorization == nil {
			return
		}
		auditParams["verification_method"] = authorization.Method
		auditParams["password_reset"] = updatePassword
		auditParams["admin_permissions_updated"] = updatedUser.AdminPermissions != nil
	}
	// 部门是否真的变化在事务内按当前行判定，避免拿事务外快照比较出错。
	departmentChanged := false

	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		// 锁序与 UpdateUserDepartment 一致：先锁部门行，再动 users 行。
		if hasDepartment && departmentID != nil {
			if err := model.EnsureDepartmentExistsWithTx(tx, *departmentID); err != nil {
				return err
			}
		}
		// 角色列复用 UpdateWithTx：root 守卫、auth_version 推进、按当前行判定
		// 都在里面。只带 Id/Role 的对象在 struct Updates 下只会写 role 列。
		if roleChanged {
			roleHolder := model.User{Id: updatedUser.Id, Role: updatedUser.Role}
			if err := roleHolder.UpdateWithTx(tx, false); err != nil {
				return err
			}
		}
		// 只写显式出现的资料列，避免把事务外快照整份写回。
		if len(profileUpdates) > 0 {
			if err := updatedUser.EditProfileWithTx(tx, profileUpdates); err != nil {
				return err
			}
		}
		if hasDepartment {
			var current struct{ DepartmentId *int }
			if err := tx.Model(&model.User{}).Where("id = ?", updatedUser.Id).
				Select("department_id").First(&current).Error; err != nil {
				return err
			}
			same := departmentID == nil && current.DepartmentId == nil
			if departmentID != nil && current.DepartmentId != nil {
				same = *departmentID == *current.DepartmentId
			}
			if !same {
				// 单列写：map/Update 不会把 nil 当零值跳过，清空能落成 NULL。
				if err := tx.Model(&model.User{}).Where("id = ?", updatedUser.Id).
					Update("department_id", departmentID).Error; err != nil {
					return err
				}
				departmentChanged = true
			}
		}
		touched, err := updateAdminPermissionsForUserInTx(c, tx, updatedUser.Id, effectiveRole, updatedUser.AdminPermissions)
		authzTouched = touched
		if err != nil {
			return err
		}
		// 没写资料列时没人回读 auth_version，这里自己刷新，
		// 否则下面的会话吊销判断看不到改角色推进的版本号。
		if len(profileUpdates) == 0 {
			return tx.First(updatedUser, updatedUser.Id).Error
		}
		return nil
	}); err != nil {
		common.ApiError(c, err)
		return
	}

	// 只对合并编辑这条路径生效：事务已提交，以下跨存储副作用失败只记日志。
	// 若返回失败，客户端会重提一次，而 DB 里角色和部门其实已经改成功了，
	// 表现为「改了却说失败」的半成功。上游纯资料编辑路径完全不受影响。
	if authzTouched {
		model.BumpSyncTopic(model.SyncTopicAuthz)
		if err := authz.ReloadPolicy(); err != nil {
			common.SysLog(common.LogText("failed to reload authz policy after user update: %s", err.Error()))
		}
	}
	if updatedUser.AuthVersion > originUser.AuthVersion {
		// 撤销 root 与 ManageUser 的 revoke_root 同口径，审计报表按 reason 过滤时不会漏。
		revokeReason := "admin_user_update"
		if roleChanged && originUser.Role == common.RoleRootUser && updatedUser.Role == common.RoleCommonUser {
			revokeReason = "admin_demote"
		}
		if _, err := model.RevokeAllUserSessions(updatedUser.Id, revokeReason); err != nil {
			common.SysLog(common.LogText("failed to revoke sessions for user %d: %s", updatedUser.Id, err.Error()))
		}
	}
	if err := model.PublishUserAuthCache(updatedUser.Id); err != nil {
		common.SysLog(common.LogText("failed to publish auth cache for user %d: %s", updatedUser.Id, err.Error()))
	}
	if roleChanged {
		if err := model.InvalidateUserTokensCache(updatedUser.Id); err != nil {
			common.SysLog(common.LogText("failed to invalidate tokens cache for user %d: %s", updatedUser.Id, err.Error()))
		}
	}
	if roleChanged {
		// 与 ManageUser 的 grant_root/revoke_root 同口径，避免按 action 过滤时漏掉这条路径。
		manageAction := "grant_root"
		if originUser.Role == common.RoleRootUser {
			manageAction = "revoke_root"
		}
		recordManageAuditFor(c, updatedUser.Id, "user.manage", map[string]any{
			"action":              manageAction,
			"username":            originUser.Username,
			"id":                  updatedUser.Id,
			"role":                updatedUser.Role,
			"verification_method": verificationMethod,
		})
	}
	if departmentChanged {
		// 占位符用 %v 渲染，传指针会写出地址；清空时为 nil。
		var auditDepartmentID any
		if departmentID != nil {
			auditDepartmentID = *departmentID
		}
		recordManageAuditFor(c, updatedUser.Id, "user.department_update", map[string]any{
			"id": updatedUser.Id, "department_id": auditDepartmentID,
		})
	}
	if len(profileUpdates) > 0 || updatedUser.AdminPermissions != nil {
		// 审计只记列名，不记值；group 会改变计费与可用模型，必须可追溯。
		changedFields := make([]string, 0, len(profileUpdates)+1)
		for column := range profileUpdates {
			changedFields = append(changedFields, column)
		}
		if updatedUser.AdminPermissions != nil {
			changedFields = append(changedFields, "admin_permissions")
		}
		slices.Sort(changedFields)
		auditParams["changed_fields"] = changedFields
		recordManageAuditFor(c, updatedUser.Id, "user.update", auditParams)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
