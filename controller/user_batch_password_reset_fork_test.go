package controller

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type batchPasswordResetResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Data    struct {
		Users []struct {
			Id          int    `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
			Password    string `json:"password"`
		} `json:"users"`
	} `json:"data"`
}

// setupBatchPasswordResetTest returns a root operator with a live session and
// count enabled common users, all holding the old password "managed-password".
func setupBatchPasswordResetTest(t *testing.T, count int) (*model.User, service.AuthIdentity, []*model.User) {
	t.Helper()
	operator, identity, first := setupAdminUserTest(t)
	targets := []*model.User{first}
	for i := 1; i < count; i++ {
		password, err := common.Password2Hash("managed-password")
		require.NoError(t, err)
		user := &model.User{
			Username: fmt.Sprintf("batch-user-%d", i), Password: password, Role: common.RoleCommonUser,
			Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
			AffCode: fmt.Sprintf("batch-aff-%d", i), GitHubId: fmt.Sprintf("batch-github-%d", i),
		}
		require.NoError(t, model.DB.Create(user).Error)
		targets = append(targets, user)
	}
	return operator, identity, targets
}

func issueBatchPasswordResetProof(t *testing.T, identity service.AuthIdentity, ids []int) string {
	t.Helper()
	payload, err := common.Marshal(service.AdminUserPasswordBatchResetContext{UserIDs: ids})
	require.NoError(t, err)
	return issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{
		Scope:   service.VerificationScopeAdminUserPasswordBatchReset,
		Context: payload,
	}, service.VerificationMethodPassword)
}

func runBatchPasswordReset(t *testing.T, identity service.AuthIdentity, role int, body, proof string) batchPasswordResetResponse {
	t.Helper()
	response := adminUserRequest(http.MethodPost, "/api/user/batch/reset_password", body, proof, identity, role, nil, BatchResetUserPasswords)
	var decoded batchPasswordResetResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &decoded), response.Body.String())
	return decoded
}

func storedPasswordHash(t *testing.T, id int) (string, int64) {
	t.Helper()
	user, err := model.GetUserById(id, true)
	require.NoError(t, err)
	return user.Password, user.AuthVersion
}

// storedPasswordHashIncludingDeleted 同上，但连软删行也读得到——锁内二次校验的
// 用例需要断言已软删目标没有被写入。
func storedPasswordHashIncludingDeleted(t *testing.T, id int) (string, int64) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Unscoped().Select("password", "auth_version").Where("id = ?", id).First(&user).Error)
	return user.Password, user.AuthVersion
}

type batchPasswordResetStoredState struct {
	hash    string
	version int64
}

func hasPasswordClasses(password string) (upper, lower, digit bool) {
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper, lower, digit
}

func TestBatchPasswordResetReturnsIndependentPasswords(t *testing.T) {
	_, identity, targets := setupBatchPasswordResetTest(t, 3)
	sortedIDs := []int{targets[0].Id, targets[1].Id, targets[2].Id}
	slices.Sort(sortedIDs)
	// 请求顺序故意与升序不同，验证响应按请求原顺序返回。
	requestIDs := []int{sortedIDs[2], sortedIDs[0], sortedIDs[1]}

	sessions := make(map[int]string, len(targets))
	oldHashes := make(map[int]string, len(targets))
	for _, target := range targets {
		bundle, err := service.CreateLoginSession(target.Id, "password", "127.0.0.1", "batch-reset-test")
		require.NoError(t, err)
		sessions[target.Id] = bundle.Session.SID
		hash, version := storedPasswordHash(t, target.Id)
		oldHashes[target.Id] = hash
		require.Equal(t, int64(1), version)
	}
	// PAT 必须原样保留：不删行、不改 scope。
	_, pat := createScopedAccessToken(t, targets[0].Id, 0, "profile:read")

	body := fmt.Sprintf(`{"ids":[%d,%d,%d]}`, requestIDs[0], requestIDs[1], requestIDs[2])
	result := runBatchPasswordReset(t, identity, common.RoleRootUser, body,
		issueBatchPasswordResetProof(t, identity, sortedIDs))
	require.True(t, result.Success, result.Message)
	require.Len(t, result.Data.Users, 3)

	seen := map[string]bool{}
	for i, user := range result.Data.Users {
		assert.Equal(t, requestIDs[i], user.Id, "response must keep the request order")
		password := user.Password
		assert.Len(t, password, 8)
		assert.False(t, seen[password], "每账号密码必须独立")
		seen[password] = true
		upper, lower, digit := hasPasswordClasses(password)
		assert.True(t, upper && lower && digit, "password %q must contain upper, lower and digit", password)
		for _, r := range password {
			if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
				t.Fatalf("password %q contains a character outside the 62-character set", password)
			}
		}

		hash, version := storedPasswordHash(t, user.Id)
		assert.Equal(t, int64(2), version, "auth_version must advance exactly once")
		assert.NotEqual(t, oldHashes[user.Id], hash)
		assert.True(t, common.ValidatePasswordAndHash(password, hash))
		assert.False(t, common.ValidatePasswordAndHash("managed-password", hash), "旧密码必须失效")

		storedSession, err := model.GetUserSessionBySID(sessions[user.Id])
		require.NoError(t, err)
		assert.Equal(t, model.UserSessionStatusRevoked, storedSession.Status)
		assert.Equal(t, "admin_batch_password_reset", storedSession.RevokedReason)
	}

	tokens, err := model.ListUserAccessTokens(targets[0].Id)
	require.NoError(t, err)
	require.Len(t, tokens, 1, "PAT 不得被删除")
	assert.Equal(t, pat.Id, tokens[0].Id)
	assert.Equal(t, []string{"profile:read"}, tokens[0].GetScopes())

	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.batch_password_reset").Last(&audit).Error)
	require.NotNil(t, audit.Other.Op)
	auditIDs, err := common.Marshal(audit.Other.Op.Params["user_ids"])
	require.NoError(t, err)
	expectedIDs, err := common.Marshal(sortedIDs)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedIDs), string(auditIDs))
	method, err := common.Marshal(audit.Other.Op.Params["verification_method"])
	require.NoError(t, err)
	assert.JSONEq(t, `"password"`, string(method))
	count, err := common.Marshal(audit.Other.Op.Params["count"])
	require.NoError(t, err)
	assert.JSONEq(t, "3", string(count))
	auditJSON, err := common.Marshal(audit)
	require.NoError(t, err)
	for password := range seen {
		assert.NotContains(t, string(auditJSON), password, "审计不得包含明文密码")
	}
	assert.NotContains(t, string(auditJSON), "managed-password")
}

func TestBatchPasswordResetRejectsUnauthorizedTargetWithoutWriting(t *testing.T) {
	operator, identity, targets := setupBatchPasswordResetTest(t, 2)
	// 操作者降为普通管理员；目标里再放一个同级管理员，逐目标校验必须整批拒绝。
	// 用裸 Update 改 role：不推进 auth_version，operator 的既有会话仍能签发 proof。
	require.NoError(t, model.DB.Model(operator).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, model.PublishUserAuthCache(operator.Id))
	sameLevelPassword, err := common.Password2Hash("managed-password")
	require.NoError(t, err)
	sameLevel := &model.User{
		Username: "same-level-admin", Password: sameLevelPassword, Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "same-level-aff",
	}
	require.NoError(t, model.DB.Create(sameLevel).Error)
	targets = append(targets, sameLevel)

	ids := []int{targets[0].Id, targets[1].Id, sameLevel.Id}
	slices.Sort(ids)
	before := make(map[int]string, len(ids))
	for _, id := range ids {
		hash, _ := storedPasswordHash(t, id)
		before[id] = hash
	}

	body := fmt.Sprintf(`{"ids":[%d,%d,%d]}`, ids[0], ids[1], ids[2])
	result := runBatchPasswordReset(t, identity, common.RoleAdminUser, body,
		issueBatchPasswordResetProof(t, identity, ids))
	assert.False(t, result.Success)
	assert.Equal(t, "No permission to update users of same or higher permission level", result.Message)

	for _, id := range ids {
		hash, version := storedPasswordHash(t, id)
		assert.Equal(t, before[id], hash, "整批拒绝时目标 id=%d 的密码不得变化", id)
		assert.Equal(t, int64(1), version, "整批拒绝时目标 id=%d 的 auth_version 不得变化", id)
	}
}

// TestBatchPasswordResetInTxRechecksTargetAfterPreflight 覆盖锁内二次授权：事务外
// 预检已经放行，目标在事务开始前被提权 / 禁用 / 软删，只有锁住后重读 role、status、
// deleted_at 的那三行能拦住。删掉其中任意一行，本用例都会变红。
func TestBatchPasswordResetInTxRechecksTargetAfterPreflight(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		mutate  func(t *testing.T, id int)
		wantErr error
	}{
		{
			name: "role escalated after preflight",
			mutate: func(t *testing.T, id int) {
				require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", id).Update("role", common.RoleRootUser).Error)
			},
			wantErr: errBatchPasswordTargetForbidden,
		},
		{
			name: "disabled after preflight",
			mutate: func(t *testing.T, id int) {
				require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", id).Update("status", common.UserStatusDisabled).Error)
			},
			wantErr: errBatchPasswordTargetDisabled,
		},
		{
			name: "soft deleted after preflight",
			mutate: func(t *testing.T, id int) {
				require.NoError(t, model.DB.Where("id = ?", id).Delete(&model.User{}).Error)
			},
			wantErr: errBatchPasswordTargetMissing,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, _, targets := setupBatchPasswordResetTest(t, 2)
			ids := []int{targets[0].Id, targets[1].Id}
			slices.Sort(ids)
			before := make(map[int]batchPasswordResetStoredState, len(ids))
			for _, id := range ids {
				hash, version := storedPasswordHashIncludingDeleted(t, id)
				before[id] = batchPasswordResetStoredState{hash: hash, version: version}
			}
			// 升序第二个目标在预检之后变脏；第一个目标仍会被处理，用来验证回滚。
			scenario.mutate(t, ids[1])

			passwordHashes := make(map[int]string, len(ids))
			for _, id := range ids {
				hash, err := common.HashAccountPassword(fmt.Sprintf("generated-%d-password", id))
				require.NoError(t, err)
				passwordHashes[id] = hash
			}
			err := model.DB.Transaction(func(tx *gorm.DB) error {
				return resetBatchPasswordsInTx(tx, common.RoleAdminUser, ids, passwordHashes)
			})
			require.ErrorIs(t, err, scenario.wantErr)

			for _, id := range ids {
				hash, version := storedPasswordHashIncludingDeleted(t, id)
				assert.Equal(t, before[id].hash, hash, "目标 id=%d 的密码不得变化", id)
				assert.Equal(t, before[id].version, version, "目标 id=%d 的 auth_version 不得变化", id)
			}
		})
	}
}

// TestBatchPasswordResetRollsBackOnWriteFailure 在第二个目标的密码 UPDATE 上注入
// 失败，证明第一个目标已经走过的 auth_version 推进与密码写入都会随事务回滚。
// 这是本功能「全或无」的核心不变量：不能只靠事务外的预检来证明。
func TestBatchPasswordResetRollsBackOnWriteFailure(t *testing.T) {
	_, identity, targets := setupBatchPasswordResetTest(t, 2)
	ids := []int{targets[0].Id, targets[1].Id}
	slices.Sort(ids)
	before := make(map[int]string, len(ids))
	for _, id := range ids {
		hash, _ := storedPasswordHash(t, id)
		before[id] = hash
	}

	const callbackName = "test:fail_second_batch_password_update"
	passwordUpdates := 0
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		// 只看「写 users.password 列」的 UPDATE：auth_version 推进的 Dest 里没有
		// password，不会被计入。本用例内除 handler 外没有别的代码写 password 列。
		if tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		values, ok := tx.Statement.Dest.(map[string]any)
		if !ok || values["password"] == nil {
			return
		}
		passwordUpdates++
		if passwordUpdates == 2 {
			tx.AddError(errors.New("forced batch password update failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	body := fmt.Sprintf(`{"ids":[%d,%d]}`, ids[0], ids[1])
	result := runBatchPasswordReset(t, identity, common.RoleRootUser, body,
		issueBatchPasswordResetProof(t, identity, ids))
	assert.False(t, result.Success)
	assert.NotEqual(t, "Invalid parameters", result.Message)
	// 注入必须真的命中：命中两次说明第一个目标写过、第二个目标写失败。
	require.Equal(t, 2, passwordUpdates, "注入的失败没有命中密码 UPDATE，本用例失去意义")

	for _, id := range ids {
		hash, version := storedPasswordHash(t, id)
		assert.Equal(t, before[id], hash, "回滚后目标 id=%d 的密码必须保持旧值", id)
		assert.Equal(t, int64(1), version, "回滚后目标 id=%d 的 auth_version 必须保持 1", id)
	}
}

func TestBatchPasswordResetRejectsMissingOrSoftDeletedTarget(t *testing.T) {
	_, identity, targets := setupBatchPasswordResetTest(t, 1)
	good := targets[0].Id
	goodHash, _ := storedPasswordHash(t, good)

	softDeleted := &model.User{
		Username: "soft-deleted-target", Password: "hash", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "soft-deleted-aff",
	}
	require.NoError(t, model.DB.Create(softDeleted).Error)
	require.NoError(t, model.DB.Delete(softDeleted).Error)

	for _, scenario := range []struct {
		name string
		id   int
	}{
		{"missing", 999999},
		{"soft deleted", softDeleted.Id},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ids := []int{good, scenario.id}
			slices.Sort(ids)
			body := fmt.Sprintf(`{"ids":[%d,%d]}`, ids[0], ids[1])
			// 不存在的 id 会让服务端在事务外就拒绝，因此无需先为它签发 proof；
			// 但请求仍带一份合法 proof，确保被拒原因确实是目标缺失而不是缺 proof。
			result := runBatchPasswordReset(t, identity, common.RoleRootUser, body,
				issueBatchPasswordResetProof(t, identity, ids))
			assert.False(t, result.Success)
			assert.Equal(t, "User does not exist", result.Message)
			hash, version := storedPasswordHash(t, good)
			assert.Equal(t, goodHash, hash, "被拒时不得改动其它目标")
			assert.Equal(t, int64(1), version)
		})
	}
}

func TestBatchPasswordResetRejectsMalformedIDs(t *testing.T) {
	_, identity, targets := setupBatchPasswordResetTest(t, 1)
	id := targets[0].Id
	goodHash, _ := storedPasswordHash(t, id)

	var tooMany strings.Builder
	tooMany.WriteString(`{"ids":[`)
	for i := 1; i <= 101; i++ {
		if i > 1 {
			tooMany.WriteString(",")
		}
		fmt.Fprintf(&tooMany, "%d", i)
	}
	tooMany.WriteString(`]}`)

	// t.Run 的 name 不能直接塞 JSON（含 / 和引号），用序号。
	index := 0
	for _, body := range []string{
		`{"ids":[]}`,
		`{}`,
		`{"ids":null}`,
		fmt.Sprintf(`{"ids":[%d,%d]}`, id, id),
		`{"ids":[0]}`,
		fmt.Sprintf(`{"ids":[-%d]}`, id),
		tooMany.String(),
	} {
		index++
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			// 不提供 proof：形状校验发生在 proof 消费之前，缺 proof 也不该改变错误 key。
			result := runBatchPasswordReset(t, identity, common.RoleRootUser, body, "")
			assert.False(t, result.Success)
			assert.Equal(t, "Invalid parameters", result.Message)
			hash, version := storedPasswordHash(t, id)
			assert.Equal(t, goodHash, hash)
			assert.Equal(t, int64(1), version)
		})
	}
}

func TestBatchPasswordResetRejectsProofForAnotherIDSet(t *testing.T) {
	_, identity, targets := setupBatchPasswordResetTest(t, 2)
	ids := []int{targets[0].Id, targets[1].Id}
	slices.Sort(ids)
	before, _ := storedPasswordHash(t, ids[0])

	// proof 只绑一个账号，请求却提交两个。
	proof := issueBatchPasswordResetProof(t, identity, ids[:1])
	body := fmt.Sprintf(`{"ids":[%d,%d]}`, ids[0], ids[1])
	result := runBatchPasswordReset(t, identity, common.RoleRootUser, body, proof)
	assert.False(t, result.Success)
	assert.Equal(t, "SECURITY_PROOF_CONTEXT_MISMATCH", result.Code)
	hash, version := storedPasswordHash(t, ids[0])
	assert.Equal(t, before, hash)
	assert.Equal(t, int64(1), version)

	// proof context 自身不允许重复 id。
	_, err := service.BindVerificationOperation(service.VerificationOperation{
		Scope:   service.VerificationScopeAdminUserPasswordBatchReset,
		Context: []byte(fmt.Sprintf(`{"user_ids":[%d,%d]}`, ids[0], ids[0])),
	})
	assert.ErrorIs(t, err, service.ErrVerificationContextInvalid)
}

func TestGenerateRandomAccountPasswordShape(t *testing.T) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	seen := make(map[string]struct{}, 200)
	for range 200 {
		password, err := common.GenerateRandomAccountPassword()
		require.NoError(t, err)
		require.Len(t, password, 8)
		upper, lower, digit := hasPasswordClasses(password)
		require.True(t, upper && lower && digit, "password %q must contain all three classes", password)
		for _, r := range password {
			require.True(t, strings.ContainsRune(charset, r), "password %q left the 62-character set", password)
		}
		require.NoError(t, common.ValidateNewAccountPassword(password))
		_, duplicate := seen[password]
		require.False(t, duplicate, "重复密码 %q", password)
		seen[password] = struct{}{}
	}
}
