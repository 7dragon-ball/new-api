package model

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
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

// TestConcurrentRevokeLeavesOneEnabledRoot 需要真实的 Postgres 行锁。
// model 包的 TestMain（model/task_cas_test.go:19）固定用 SQLite in-memory，
// 这里单独开一条 Postgres 连接，只迁移本用例需要的 users 表。
func TestConcurrentRevokeLeavesOneEnabledRoot(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	// 这个用例会 DROP 并重建 users 表，必须独占库。TEST_POSTGRES_DSN 若被误指向
	// 共享实例（或主库），一次 `go test` 就清空所有账号。库名里不含 test 一律拒跑。
	require.True(t, strings.Contains(strings.ToLower(dsn), "test"),
		"TEST_POSTGRES_DSN 必须指向专用测试库（库名需含 test），该用例会 DROP users 表")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	// 至少 2 条连接，否则两个事务串行执行，锁竞争根本没发生。
	sqlDB.SetMaxOpenConns(4)

	// lockForUpdate（model/locking.go:16）靠 common.UsingMainDatabase(SQLite) 决定
	// 要不要发 FOR UPDATE。不改这里的库类型，这条测试跑在 Postgres 上也拿不到行锁。
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypeSQLite)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.SetDatabaseTypes(previousMain, previousLog)
	})

	require.NoError(t, db.Migrator().DropTable(&User{}))
	require.NoError(t, db.AutoMigrate(&User{}))
	t.Cleanup(func() { db.Migrator().DropTable(&User{}) })

	first := createTestRootUser(t, "root-a", common.UserStatusEnabled)
	second := createTestRootUser(t, "root-b", common.UserStatusEnabled)

	type outcome struct {
		id  int
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, id := range []int{first.Id, second.Id} {
		go func(id int) {
			<-start
			change := User{Id: id, Role: common.RoleCommonUser}
			results <- outcome{id: id, err: DB.Transaction(func(tx *gorm.DB) error {
				return change.UpdateWithTx(tx, false)
			})}
		}(id)
	}
	close(start)

	successes, rejected := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
			continue
		}
		rejected++
		// 后到者的 FOR UPDATE 阻塞到前者提交，再按 READ COMMITTED 重新求值，
		// 只看到 1 个启用 root，于是拿到守卫的错误。
		assert.ErrorIs(t, result.err, ErrLastEnabledRootUser, "user %d", result.id)
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, rejected)

	count, err := CountEnabledRootUsers(DB)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}
