package model

import (
	"strings"
	"time"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

func createTestRootUser(t *testing.T, username string, status int) User {
	t.Helper()
	user := User{
		Username:    username,
		Password:    "unused-password-hash",
		Role:        common.RoleRootUser,
		Status:      status,
		Group:       "default",
		AuthVersion: 1,
		AffCode:     username + "-aff",
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func TestCountEnabledRootUsersExcludesDisabledAndDeleted(t *testing.T) {
	setupUserUpdateTestState(t)
	createTestRootUser(t, "root-a", common.UserStatusEnabled)
	createTestRootUser(t, "root-b", common.UserStatusEnabled)
	createTestRootUser(t, "root-disabled", common.UserStatusDisabled)

	count, err := CountEnabledRootUsers(DB)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	require.NoError(t, DB.Exec("UPDATE users SET deleted_at = CURRENT_TIMESTAMP WHERE username = ?", "root-b").Error)
	count, err = CountEnabledRootUsers(DB)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}

func TestLockEnabledRootIDsEmitsRowLockAndSkipsDeleted(t *testing.T) {
	dummyDB, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)

	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	})

	var ids []int
	stmt := lockForUpdate(dummyDB.Model(&User{})).
		Where("role = ? AND status = ?", common.RoleRootUser, common.UserStatusEnabled).
		Order("id").
		Pluck("id", &ids).Statement
	// DummyDialector 会用反引号引用标识符，断言前先去掉
	sql := strings.ReplaceAll(stmt.SQL.String(), "`", "")
	assert.Contains(t, sql, "FOR UPDATE")
	assert.Contains(t, sql, "deleted_at IS NULL")
}

func TestLastEnabledRootUserCannotBeDemoted(t *testing.T) {
	setupUserUpdateTestState(t)
	root := createTestRootUser(t, "only-root", common.UserStatusEnabled)

	change := User{Id: root.Id, Role: common.RoleCommonUser}
	err := DB.Transaction(func(tx *gorm.DB) error {
		return change.UpdateWithTx(tx, false)
	})
	require.ErrorIs(t, err, ErrLastEnabledRootUser)

	var after User
	require.NoError(t, DB.First(&after, root.Id).Error)
	assert.Equal(t, common.RoleRootUser, after.Role)
	assert.EqualValues(t, 1, after.AuthVersion)
}

func TestLastEnabledRootUserCannotBeDisabled(t *testing.T) {
	setupUserUpdateTestState(t)
	root := createTestRootUser(t, "only-root", common.UserStatusEnabled)

	change := User{Id: root.Id, Status: common.UserStatusDisabled}
	err := DB.Transaction(func(tx *gorm.DB) error {
		return change.UpdateWithTx(tx, false)
	})
	require.ErrorIs(t, err, ErrLastEnabledRootUser)

	var after User
	require.NoError(t, DB.First(&after, root.Id).Error)
	assert.Equal(t, common.UserStatusEnabled, after.Status)
}

func TestDemotingOneOfTwoRootsSucceeds(t *testing.T) {
	setupUserUpdateTestState(t)
	first := createTestRootUser(t, "root-a", common.UserStatusEnabled)
	createTestRootUser(t, "root-b", common.UserStatusEnabled)

	change := User{Id: first.Id, Role: common.RoleCommonUser}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return change.UpdateWithTx(tx, false)
	}))

	var after User
	require.NoError(t, DB.First(&after, first.Id).Error)
	assert.Equal(t, common.RoleCommonUser, after.Role)
}

func TestGuardedUpdateLeavesNonRootUserAlone(t *testing.T) {
	setupUserUpdateTestState(t)
	createTestRootUser(t, "only-root", common.UserStatusEnabled)
	member := createAccessTokenTestUser(t, "plain-user")

	change := User{Id: member.Id, Role: common.RoleAdminUser}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return change.UpdateWithTx(tx, false)
	}))

	var after User
	require.NoError(t, DB.First(&after, member.Id).Error)
	assert.Equal(t, common.RoleAdminUser, after.Role)
}

func TestLastEnabledRootUserCannotBeDeleted(t *testing.T) {
	setupUserUpdateTestState(t)
	root := createTestRootUser(t, "only-root", common.UserStatusEnabled)

	_, err := (&User{Id: root.Id}).Delete()
	require.ErrorIs(t, err, ErrLastEnabledRootUser)

	var count int64
	require.NoError(t, DB.Model(&User{}).Where("id = ?", root.Id).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestDeletingDisabledRootIsAllowed(t *testing.T) {
	setupUserUpdateTestState(t)
	createTestRootUser(t, "active-root", common.UserStatusEnabled)
	disabled := createTestRootUser(t, "disabled-root", common.UserStatusDisabled)

	_, err := (&User{Id: disabled.Id}).Delete()
	require.NoError(t, err)
}

func TestDisabledRootDeletionThenLastActiveDemotionFails(t *testing.T) {
	setupUserUpdateTestState(t)
	active := createTestRootUser(t, "active-root", common.UserStatusEnabled)
	disabled := createTestRootUser(t, "disabled-root", common.UserStatusDisabled)

	_, err := (&User{Id: disabled.Id}).Delete()
	require.NoError(t, err)

	change := User{Id: active.Id, Role: common.RoleCommonUser}
	err = DB.Transaction(func(tx *gorm.DB) error {
		return change.UpdateWithTx(tx, false)
	})
	require.ErrorIs(t, err, ErrLastEnabledRootUser)
}

func TestRootSelfDeleteKeepsUpstreamError(t *testing.T) {
	setupUserUpdateTestState(t)
	root := createTestRootUser(t, "only-root", common.UserStatusEnabled)

	// delete 在 identity != nil 时先跑 ValidateAuthSessionWithTx，伪造的 identity 会
	// 在到达守卫前就返回 ErrUserSessionInactive。必须建一条真实可用的会话。
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&UserSession{
		SID: "root-self-delete", UserID: root.Id, Version: 1, UserAuthVersion: 1,
		Status: UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)
	identity := AuthSessionIdentity{
		UserID: root.Id, SessionID: "root-self-delete",
		UserAuthVersion: 1, SessionVersion: 1,
	}

	_, err := DeleteUserForSession(identity)
	// 断言的是上游错误，不是新守卫的错误：自删路径必须保持原语义。
	require.ErrorIs(t, err, ErrCannotDeleteRootUser)

	var after User
	require.NoError(t, DB.First(&after, root.Id).Error)
	assert.Equal(t, common.RoleRootUser, after.Role)
}
