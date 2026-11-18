package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] proof 上下文的键集必须钉死：同一份意图在不同客户端要算出同一个
// hash，也不能让未绑定的字段搭便车绕过验证。
func TestBindAdminUserManagementContext(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context string
		valid   bool
	}{
		{"role and department", `{"user_id":7,"changes":["department","role"],"role":100,"department_id":3}`, true},
		{"clear department", `{"user_id":7,"changes":["department"],"role":0,"department_id":null}`, true},
		{"with permission matrix", `{"user_id":7,"changes":["role"],"role":1,"department_id":null,"admin_permissions":{"users":{"read":true}}}`, true},
		{"extra key without matrix", `{"user_id":7,"changes":["role"],"role":1,"department_id":null,"extra":1}`, false},
		{"extra key with matrix", `{"user_id":7,"changes":["role"],"role":1,"department_id":null,"admin_permissions":{"users":{"read":true}},"extra":1}`, false},
		{"missing fixed key", `{"user_id":7,"changes":["role"],"role":1}`, false},
		{"department id when role only", `{"user_id":7,"changes":["role"],"role":1,"department_id":3}`, false},
		{"role when department only", `{"user_id":7,"changes":["department"],"role":100,"department_id":null}`, false},
		{"empty changes", `{"user_id":7,"changes":[],"role":0,"department_id":null}`, false},
		{"unsorted changes", `{"user_id":7,"changes":["role","department"],"role":100,"department_id":3}`, false},
		{"duplicate changes", `{"user_id":7,"changes":["role","role"],"role":100,"department_id":null}`, false},
		{"unknown change", `{"user_id":7,"changes":["password"],"role":0,"department_id":null}`, false},
		{"invalid role", `{"user_id":7,"changes":["role"],"role":10,"department_id":null}`, false},
		{"negative department", `{"user_id":7,"changes":["department"],"role":0,"department_id":-1}`, false},
		{"empty matrix", `{"user_id":7,"changes":["role"],"role":1,"department_id":null,"admin_permissions":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BindVerificationOperation(VerificationOperation{
				Scope:   VerificationScopeAdminUserManagement,
				Context: []byte(tc.context),
			})
			if tc.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrVerificationContextInvalid)
		})
	}
}
