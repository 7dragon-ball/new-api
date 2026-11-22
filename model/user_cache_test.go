package model

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UserBase 新增字段必须体现在缓存往返与上下文写入两处。
func TestUserBaseCarriesDisplayNameAndDepartment(t *testing.T) {
	truncateTables(t)
	dept := &Department{Id: 7, Name: "技术部"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(&Department{}, 7) })
	deptID := dept.Id
	user := &User{Id: 42, Username: "zhangwei", DisplayName: "张三", Group: "default", DepartmentId: &deptID}
	base := user.ToBaseUser()
	assert.Equal(t, "张三", base.DisplayName, "显示名必须进缓存，否则日志热路径只能查库")
	// 部门 id 刻意不进 UserBase：结构体相等用于缓存一致性判断，指针字段会误判。
	assert.NotContains(t, fmt.Sprintf("%+v", *base), "DepartmentId")
}

// 回归：UserBase 里不能出现指针字段。
//
// PublishUserAuthCache 用 `*a == *b` 比较两次独立 DB 读取的结果来判断
// 「元数据是否还在变」。带着 *int 字段时，同一个人两次读取的指针必然不同，
// 会被判成永远在变，函数跑满 3 次重试后返回错误——而它被改用户、批量重置
// 密码、passkey、2FA 等十几处调用。
func TestPublishUserAuthCacheSucceedsWithDepartment(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	dept := &Department{Name: "回归部门"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(dept) })
	deptID := dept.Id
	user := User{Username: "publish-regression", Group: "default", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, AuthVersion: 1, DepartmentId: &deptID}
	require.NoError(t, DB.Create(&user).Error)

	// 两次 GetUserById 得到不同指针；按值相等必须被判为相等，否则这里会报
	// 「user metadata changed repeatedly during cache refresh」。
	require.NoError(t, PublishUserAuthCache(user.Id))
}

// 缓存是逐字段写 Redis 的（Lua 脚本里列了字段名），光改结构体不会让新字段落库。
// 这条用例真的走一遍 populate → HGETALL，钉住脚本与结构体同步，否则上线后
// 字段永远读回空串，而 MAC 又因为结构体变了而对不上。
func TestUserBaseIdentitySurvivesCacheRoundTrip(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	dept := &Department{Id: 7, Name: "技术部"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(&Department{}, 7) })
	deptID := dept.Id
	user := User{Username: "zhangwei", DisplayName: "张三", Group: "default",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AuthVersion: 1, DepartmentId: &deptID}
	require.NoError(t, DB.Create(&user).Error)

	require.NoError(t, populateUserCache(user))

	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "张三", cached.DisplayName)
	assert.Equal(t, "技术部", cached.DepartmentName, "部门名由 populateUserCache 回查字典表补上")

	// 上下文是日志热路径唯一的取值来源，漏写就等于静默写空快照。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	cached.WriteContext(c)
	assert.Equal(t, "张三", c.GetString(string(constant.ContextKeyUserDisplayName)))
	assert.Equal(t, "技术部", c.GetString(string(constant.ContextKeyUserDepartmentName)))
}

// 未分配部门的用户：部门名必须是空串，不能退化成 id 或其他占位。
func TestUserBaseNilDepartmentRoundTrip(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := User{Username: "no-dept", Group: "default", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, DB.Create(&user).Error)

	require.NoError(t, populateUserCache(user))

	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "", cached.DepartmentName)
}

// 结构变更后旧缓存必须被判定为过期，否则老数据会一直缺这些字段。
func TestUserBaseSchemaVersionBumped(t *testing.T) {
	assert.Equal(t, 3, userCacheSchemaVersion,
		"UserBase 加字段必须升版本号，否则旧 Redis 缓存继续通过 MAC 校验")
}

// 回归：updateUserCache 不能把部门名冲成空串。
//
// 它覆盖改密码 / 改角色 / 改部门 / 2FA / passkey 等十几处调用；只写
// ToBaseUser() 会让「用户改过一次资料」之后一个缓存周期内写入的日志全无部门。
func TestUpdateUserCacheKeepsDepartmentName(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	dept := &Department{Name: "技术部"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(dept) })
	deptID := dept.Id
	user := User{Username: "upd-dept", DisplayName: "张三", Group: "default",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AuthVersion: 1, DepartmentId: &deptID}
	require.NoError(t, DB.Create(&user).Error)

	require.NoError(t, populateUserCache(user))
	require.NoError(t, updateUserCache(user))

	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "技术部", cached.DepartmentName, "改资料后部门名必须还在")
}

// 回归：GetUserCache 冷启动（Redis miss）的返回值也要带部门名。
//
// 旧实现 miss 分支先 populateUserCache 灌对了 Redis，却返回 user.ToBaseUser()，
// 于是每个缓存周期都有一条请求拿到空部门。
func TestGetUserCacheColdMissCarriesDepartmentName(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	dept := &Department{Name: "冷启动部"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(dept) })
	deptID := dept.Id
	user := User{Username: "cold-miss", DisplayName: "李四", Group: "default",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AuthVersion: 1, DepartmentId: &deptID}
	require.NoError(t, DB.Create(&user).Error)

	cold, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "冷启动部", cold.DepartmentName, "冷启动返回值不能是空部门")

	warm, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "冷启动部", warm.DepartmentName)
}
