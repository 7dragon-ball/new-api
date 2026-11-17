package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
