package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// setupMultiRootManageTest 准备 manage 路径的全部前置：一个真实的 root 操作者，
// 以及已初始化的 authz enforcer（demote 分支会调 authz.ReloadPolicy）。
func setupMultiRootManageTest(t *testing.T) (*gorm.DB, service.AuthIdentity) {
	t.Helper()
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))
	operator := createQuotaTestOperator(t, db, common.RoleRootUser)
	require.NoError(t, model.PublishUserAuthCache(operator.Id))
	bundle, err := service.CreateLoginSession(operator.Id, "password", "127.0.0.1", "multi-root-test")
	require.NoError(t, err)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	return db, identity
}

func createManagedCommonUser(t *testing.T, db *gorm.DB, username string) model.User {
	t.Helper()
	user := model.User{
		Username: username, Password: "managed-password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: username + "-aff",
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

// manageAsOperator 走完整 ManageUser 路径：先用 manage scope 的 proof，再调 handler。
// context 拼法与上游 controller/user_manage_test.go:143 一致。
func manageAsOperator(t *testing.T, identity service.AuthIdentity, targetID int, action string) securityEnrollmentResponse {
	t.Helper()
	context := fmt.Sprintf(`{"user_id":%d,"action":%q}`, targetID, action)
	proof := issueSecurityEnrollmentProof(t, identity,
		service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(context)},
		service.VerificationMethodPassword)
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":%q}`, targetID, action), identity, proof)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}

// deleteAsOperator 的 action 走的是独立 scope（controller/user.go 的 case "delete"），
// 拿 manage scope 的 proof 会在 proof 阶段就返回 SECURITY_PROOF_SCOPE_MISMATCH，
// 压根到不了模型守卫。
func deleteAsOperator(t *testing.T, identity service.AuthIdentity, targetID int) securityEnrollmentResponse {
	t.Helper()
	proof := issueSecurityEnrollmentProof(t, identity,
		service.VerificationOperation{Scope: service.VerificationScopeAdminUserDelete, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, targetID))},
		service.VerificationMethodPassword)
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, targetID), identity, proof)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return result
}

func TestManageUserGrantRootAndRevokeRoot(t *testing.T) {
	db, identity := setupMultiRootManageTest(t)
	target := createManagedCommonUser(t, db, "managed-to-promote")

	// 此刻系统里只有操作者 1 个 root。grant_root 让 target 也变 root（2 个），
	// revoke_root 才有空间成功。
	result := manageAsOperator(t, identity, target.Id, "grant_root")
	require.True(t, result.Success, "message=%s", result.Message)

	var after model.User
	require.NoError(t, db.First(&after, target.Id).Error)
	assert.Equal(t, common.RoleRootUser, after.Role)

	result = manageAsOperator(t, identity, target.Id, "revoke_root")
	require.True(t, result.Success, "message=%s", result.Message)
	require.NoError(t, db.First(&after, target.Id).Error)
	assert.Equal(t, common.RoleCommonUser, after.Role)
}

func TestManageUserCannotRemoveLastEnabledRoot(t *testing.T) {
	db, identity := setupMultiRootManageTest(t)
	var operator model.User
	require.NoError(t, db.First(&operator, identity.UserID).Error)
	require.Equal(t, common.RoleRootUser, operator.Role)

	result := manageAsOperator(t, identity, operator.Id, "revoke_root")
	assert.False(t, result.Success)
	assert.Contains(t, result.Message, "last enabled administrator")

	var after model.User
	require.NoError(t, db.First(&after, operator.Id).Error)
	assert.Equal(t, common.RoleRootUser, after.Role)
}

func TestManageUserCannotDisableOrDeleteLastEnabledRoot(t *testing.T) {
	db, identity := setupMultiRootManageTest(t)
	var operator model.User
	require.NoError(t, db.First(&operator, identity.UserID).Error)

	// disable 在 manage scope 下，守卫应把它拒成 "last enabled administrator"
	disable := manageAsOperator(t, identity, operator.Id, "disable")
	assert.False(t, disable.Success)
	assert.Contains(t, disable.Message, "last enabled administrator")

	// delete 走独立 scope（见 deleteAsOperator 的注释）
	deleted := deleteAsOperator(t, identity, operator.Id)
	assert.False(t, deleted.Success)
	assert.Contains(t, deleted.Message, "last enabled administrator")

	var after model.User
	require.NoError(t, db.First(&after, operator.Id).Error)
	assert.Equal(t, common.RoleRootUser, after.Role)
	assert.Equal(t, common.UserStatusEnabled, after.Status)
}

func TestManageUserDemoteStillRejectsRoot(t *testing.T) {
	db, identity := setupMultiRootManageTest(t)
	var operator model.User
	require.NoError(t, db.First(&operator, identity.UserID).Error)

	// demote 对 root 的硬拒保持上游语义，多 root 撤销走 revoke_root。
	result := manageAsOperator(t, identity, operator.Id, "demote")
	assert.False(t, result.Success)
	assert.Contains(t, result.Message, "Cannot demote super administrator user")
}

func TestManageUserGrantRootRequiresProof(t *testing.T) {
	db, identity := setupMultiRootManageTest(t)
	target := createManagedCommonUser(t, db, "managed-no-proof")

	// 只发 body、不带 X-Security-Proof，直接调 handler。
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"grant_root"}`, target.Id), identity, "")
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)

	var after model.User
	require.NoError(t, db.First(&after, target.Id).Error)
	assert.Equal(t, common.RoleCommonUser, after.Role)
}

// performVerifiedManageUserRequestAs 与上游 performVerifiedManageUserRequest 相同，
// 只把 context 里的 role 参数化——那条把 role 写死成 RoleRootUser，测不了
// 「非 root 调用 grant_root」。
func performVerifiedManageUserRequestAs(t *testing.T, body string, identity service.AuthIdentity, proof string, role int) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		c.Request.Header.Set("X-Security-Proof", proof)
	}
	c.Set("id", identity.UserID)
	c.Set("role", role)
	c.Set("username", "manage-operator")
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(common.RequestIdKey, "multi-root-test-request")
	ManageUser(c)
	return recorder
}

// role=10 是上游 promote 能造出的「普通管理员」。产品模型里不该存在，但历史库里
// 可能有。它的 proof 层（state.Role < RoleAdminUser）与 canManageTargetRole(10,1)
// 都会放行，所以 grant_root 里那句 myRole 检查是链路上唯一的一道闸。
func TestManageUserAdminCannotGrantRoot(t *testing.T) {
	db, _ := setupMultiRootManageTest(t)

	admin := model.User{
		Username: "legacy-admin", Password: "legacy-admin-hash", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "legacy-admin-aff",
	}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, model.PublishUserAuthCache(admin.Id))
	bundle, err := service.CreateLoginSession(admin.Id, "password", "127.0.0.1", "multi-root-test")
	require.NoError(t, err)
	adminIdentity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	target := createManagedCommonUser(t, db, "managed-by-admin")
	context := fmt.Sprintf(`{"user_id":%d,"action":"grant_root"}`, target.Id)
	proof := issueSecurityEnrollmentProof(t, adminIdentity,
		service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(context)},
		service.VerificationMethodPassword)

	recorder := performVerifiedManageUserRequestAs(t,
		fmt.Sprintf(`{"id":%d,"action":"grant_root"}`, target.Id), adminIdentity, proof, common.RoleAdminUser)
	var result securityEnrollmentResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Contains(t, result.Message, "Regular administrators cannot grant super administrator")

	var after model.User
	require.NoError(t, db.First(&after, target.Id).Error)
	assert.Equal(t, common.RoleCommonUser, after.Role)
}
