package model

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 前端按「缺值给 null、不是缺键」约定区分「没有手机号」与「字段没返回」。
// 加 omitempty 会让 nil 指针的整个键消失，必须拦住。
func TestUserWithoutPhoneSerializesNullNotMissingKey(t *testing.T) {
	user := &User{Id: 1, Username: "u", Phone: nil}
	payload, err := json.Marshal(user)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	value, ok := decoded["phone"]
	assert.True(t, ok, "phone 键必须存在，不能因 omitempty 消失")
	assert.Nil(t, value, "phone 应为 null")
}

func TestUserPhoneRoundTrips(t *testing.T) {
	truncateTables(t)
	phone := "13800138000"
	user := &User{
		Username: "phone-holder", Password: "password123",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		Group: "default", AffCode: "phone-aff", Phone: &phone,
	}
	require.NoError(t, DB.Create(user).Error)

	got, err := GetUserById(user.Id, false)
	require.NoError(t, err)
	require.NotNil(t, got.Phone)
	assert.Equal(t, phone, *got.Phone)
}

// 部分唯一索引：只约束非 NULL。多个无号码用户必须能共存。
func TestMultipleUsersMayHaveNullPhone(t *testing.T) {
	truncateTables(t)
	for _, name := range []string{"no-phone-a", "no-phone-b", "no-phone-c"} {
		user := &User{
			Username: name, Password: "password123",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
			Group: "default", AffCode: name + "-aff",
		}
		require.NoError(t, DB.Create(user).Error, "%s 无手机号必须能建", name)
	}
}

func TestDuplicateNonEmptyPhoneIsRejected(t *testing.T) {
	truncateTables(t)
	phone := "13900139000"
	mk := func(name string) *User {
		return &User{
			Username: name, Password: "password123",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
			Group: "default", AffCode: name + "-aff", Phone: &phone,
		}
	}
	require.NoError(t, DB.Create(mk("dup-a")).Error)

	err := DB.Create(mk("dup-b")).Error
	require.Error(t, err, "相同非空手机号必须被唯一索引拒绝")
	assert.True(t, IsPhoneDuplicate(err), "应被 IsPhoneDuplicate 识别，实际 %v", err)
}
