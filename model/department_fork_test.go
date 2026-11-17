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
