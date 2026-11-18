package service

import (
	"encoding/json"
	"slices"

	"github.com/QuantumNous/new-api/common"
)

// [FORK] AdminUserManagementContext binds one combined edit to the exact fields
// it changes. Changes is a sorted set drawn from "department" and "role"; a nil
// DepartmentID means "clear the department" when the set contains department,
// and "leave it untouched" otherwise. Role is the requested target role, or 0
// when the role is not being changed.
type AdminUserManagementContext struct {
	UserID           int                        `json:"user_id"`
	Changes          []string                   `json:"changes"`
	Role             int                        `json:"role"`
	DepartmentID     *int                       `json:"department_id"`
	AdminPermissions map[string]map[string]bool `json:"admin_permissions,omitempty"`
}

// [FORK] 单独放校验规则，让上游 BindVerificationOperation 只多一行 case。
// 键集固定为四个字段，改权限矩阵时多一个 admin_permissions；未改的项必须取
// 固定值，否则同一份意图在不同客户端会算出不同 hash，proof 验不过。
func bindAdminUserManagementContext(operation VerificationOperation, fields map[string]json.RawMessage) (any, error) {
	invalid := ErrVerificationContextInvalid
	_, hasMatrix := fields["admin_permissions"]
	if len(fields) != 4 && !(len(fields) == 5 && hasMatrix) {
		return nil, invalid
	}
	var context AdminUserManagementContext
	if common.Unmarshal(operation.Context, &context) != nil || context.UserID <= 0 || len(context.Changes) == 0 {
		return nil, invalid
	}
	if len(context.Changes) > 2 || !slices.IsSorted(context.Changes) ||
		len(slices.Compact(slices.Clone(context.Changes))) != len(context.Changes) {
		return nil, invalid
	}
	hasRole := slices.Contains(context.Changes, "role")
	hasDepartment := slices.Contains(context.Changes, "department")
	for _, change := range context.Changes {
		if change != "role" && change != "department" {
			return nil, invalid
		}
	}
	if hasRole {
		if context.Role != common.RoleCommonUser && context.Role != common.RoleRootUser {
			return nil, invalid
		}
	} else if context.Role != 0 {
		return nil, invalid
	}
	if !hasDepartment && context.DepartmentID != nil {
		return nil, invalid
	}
	if context.DepartmentID != nil && *context.DepartmentID <= 0 {
		return nil, invalid
	}
	if hasMatrix && len(context.AdminPermissions) == 0 {
		return nil, invalid
	}
	return context, nil
}
