package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UserBase 新增字段必须体现在缓存往返与上下文写入两处。
func TestUserBaseCarriesDisplayName(t *testing.T) {
	user := &User{Id: 42, Username: "zhangwei", DisplayName: "张三", Group: "default"}
	base := user.ToBaseUser()
	assert.Equal(t, "张三", base.DisplayName, "显示名必须进缓存，否则日志热路径只能查库")
	// 部门名不在这里取：User 只有部门 id，名字由 baseUserWithDepartment 查字典表补上。
	assert.Equal(t, "", base.DepartmentName)
}

// 回归：UserBase 里不能出现指针字段。PublishUserAuthCache 用 `*a == *b` 比较两次
// 独立 DB 读取的结果，`*int` 字段会让同一个人永远判不等，跑满重试后报错。
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

// 缓存是逐字段写 Redis 的（Lua 脚本里列了字段名），光加结构体字段不会落库：
// 走一遍 populate → HGETALL，钉住脚本与结构体同步。
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

// 结构变更后旧缓存必须被判为过期，否则老数据会一直缺这些字段。版本号本身不单独立
// 用例（它是写进缓存自己 MAC 里的私有常量），真实约束由下面的往返用例覆盖。

// 回归：updateUserCache 不能把部门名冲成空串——它覆盖改密码、改角色、改部门、
// 2FA、passkey 等十几处调用，只写 ToBaseUser() 会让这些操作之后的日志全无部门。
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

// 回归：GetUserCache 冷启动（Redis miss）的返回值也要带部门名，与写进 Redis 的是
// 同一份——直接返回 ToBaseUser() 会让每个缓存周期的第一条请求拿到空部门。
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

// Redis 关闭时 GetUserCache 是鉴权热路径（每个请求都调），不能去查 department：
// 没有缓存复用结果，等于每个请求多一次查询。部门名此时留空。
func TestGetUserCacheWithoutRedisSkipsDepartmentLookup(t *testing.T) {
	truncateTables(t)
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = prevRedis })

	dept := &Department{Name: "热路径部"}
	require.NoError(t, DB.Create(dept).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(dept) })
	deptID := dept.Id
	user := User{Username: "no-redis", DisplayName: "王五", Group: "default",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AuthVersion: 1, DepartmentId: &deptID}
	require.NoError(t, DB.Create(&user).Error)

	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "王五", cached.DisplayName, "显示名不依赖字典表，必须照常返回")
	assert.Equal(t, "", cached.DepartmentName, "Redis 关闭时不为部门名额外查库")
}
