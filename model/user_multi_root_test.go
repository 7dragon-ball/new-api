package model

import (
	"strings"
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
