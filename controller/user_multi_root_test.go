package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateUserRootCanCreateRoot(t *testing.T) {
	_, identity, _ := setupAdminUserTest(t)
	proof := issueSecurityEnrollmentProof(t, identity,
		service.VerificationOperation{Scope: service.VerificationScopeAdminUserCreate, Context: []byte(`{"role":100}`)},
		service.VerificationMethodPassword)

	body := `{"username":"second-root","password":"root-password-1","role":100}`
	response := adminUserRequest(http.MethodPost, "/api/user/", body, proof, identity, common.RoleRootUser, nil, CreateUser)

	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.True(t, result.Success, "message=%s", result.Message)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("role = ?", common.RoleRootUser).Count(&count).Error)
	assert.EqualValues(t, 2, count)
}

func TestCreateUserAdminCannotCreateRoot(t *testing.T) {
	_, identity, _ := setupAdminUserTest(t)
	// role=10 的 operator 拿不到 admin.user.create proof 也无所谓：
	// 等级检查必须在 proof 之前就拒掉，否则这本身就是提权漏洞。
	body := `{"username":"escalated-root","password":"root-password-1","role":100}`
	response := adminUserRequest(http.MethodPost, "/api/user/", body, "", identity, common.RoleAdminUser, nil, CreateUser)

	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	assert.False(t, result.Success)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "escalated-root").Count(&count).Error)
	assert.Zero(t, count)
}
