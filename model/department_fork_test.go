package model

import (
	"fmt"
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

// 锁必须真的发 FOR UPDATE。GORM v2 会静默忽略旧的 Set("gorm:query_option")，
// lockForUpdate 是唯一正确的写法；这条用例钉住 LockDepartmentWithTx 走的是它。
func TestLockDepartmentWithTxEmitsRowLock(t *testing.T) {
	dummyDB, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)

	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })

	var captured string
	require.NoError(t, dummyDB.Callback().Query().After("gorm:query").Register("test:capture_lock_sql", func(db *gorm.DB) {
		captured = db.Statement.SQL.String()
	}))
	_, err = LockDepartmentWithTx(dummyDB, 7)
	require.NoError(t, err)

	sql := strings.ReplaceAll(captured, "`", "")
	assert.Contains(t, sql, "FROM department")
	assert.Contains(t, sql, "FOR UPDATE", "锁必须在 SQL 里，否则并发删部门检查形同虚设")
}

func TestDepartmentTableNameIsSingular(t *testing.T) {
	// 手写 SQL（删除计数、部门用量聚合）都写 department，靠 TableName 钉死单数。
	assert.Equal(t, "department", Department{}.TableName())
}

func TestDepartmentBeforeSaveNormalizesName(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "技术部", want: "技术部"},
		{in: "  技术部  ", want: "技术部"},
		{in: "技术部　", want: "技术部"}, // 全角空格也归一
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: strings.Repeat("部", 31), wantErr: true},
		{in: strings.Repeat("部", 30), want: strings.Repeat("部", 30)},
	}
	for _, tc := range tests {
		dept := &Department{Name: tc.in}
		err := dept.BeforeSave(DB)
		if tc.wantErr {
			assert.ErrorIs(t, err, ErrDepartmentNameInvalid, "input=%q", tc.in)
			continue
		}
		require.NoError(t, err, "input=%q", tc.in)
		assert.Equal(t, tc.want, dept.Name, "input=%q", tc.in)
	}
}

func TestDepartmentBeforeSaveCountsRunesNotBytes(t *testing.T) {
	// 30 个汉字是 90 字节；若按字节算会被误判超长。
	name := strings.Repeat("部", 30)
	dept := &Department{Name: name}
	require.NoError(t, dept.BeforeSave(DB))
	assert.Equal(t, name, dept.Name)
}

func TestDepartmentNameUniqueIndexRejectsDuplicate(t *testing.T) {
	truncateDepartments(t)
	first := &Department{Name: "技术部"}
	require.NoError(t, DB.Create(first).Error)
	// 归一后同名也必须被唯一索引挡住
	second := &Department{Name: "  技术部 "}
	assert.Error(t, DB.Create(second).Error)
}

func truncateDepartments(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { DB.Exec("DELETE FROM department") })
}

// 部门表在 PostgreSQL 上由 goose 迁移建（model/goose_fork.go）。这条用例钉住四件事：
// 迁移能建出表和唯一索引、重复执行幂等、建出的表能挡住重名、非 PG 库直接拒绝。
//
// 需要 TEST_POSTGRES_DSN，与仓库里其它 PG 用例同一个开关。用独立 schema 隔离，
// 因为用例要真的建表，而共享的测试库可能已经有 department。
func TestForkMigrationsOnPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the fork goose migration test")
	}

	previousDB := DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		DB = previousDB
		common.SetDatabaseTypes(previousMain, previousLog)
	})

	schema := fmt.Sprintf("dept_goose_%d", time.Now().UnixNano())
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// SET search_path 只对执行它的那条连接有效。锁成单连接，后续查询（含 goose 的）
	// 才都落在隔离 schema 里，不会把表建到共享测试库的 public 下。
	sqlDB.SetMaxOpenConns(1)

	require.NoError(t, db.Exec("CREATE SCHEMA IF NOT EXISTS "+schema).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE") })

	DB = db
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)

	require.NoError(t, runForkMigrations())
	require.True(t, db.Migrator().HasTable(&Department{}))
	require.True(t, db.Migrator().HasIndex(&Department{}, "uk_department_name"))

	// 幂等：多节点启动会各跑一次，第二次不能报错也不能重建表。
	require.NoError(t, runForkMigrations())

	dept := &Department{Name: "技术部"}
	require.NoError(t, db.Create(dept).Error)
	assert.NotZero(t, dept.Id)
	assert.Error(t, db.Create(&Department{Name: "技术部"}).Error, "迁移建出的唯一索引必须生效")
}

// 非 PostgreSQL 的库不该跑 PG 方言 SQL。这条拦住「将来有人挪动调用点」的失误。
func TestForkMigrationsRejectNonPostgres(t *testing.T) {
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	err := runForkMigrations()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PostgreSQL-only")
}
