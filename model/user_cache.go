package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const userCacheSchemaVersion = 3

type UserBase struct {
	Id             int    `json:"id"`
	Group          string `json:"group"`
	Email          string `json:"email"`
	Quota          int    `json:"quota"`
	Status         int    `json:"status"`
	Role           int    `json:"role"`
	Username       string `json:"username"`
	Setting        string `json:"setting"`
	AuthVersion    int64  `json:"-"`
	CacheSchema    int    `json:"-"`
	CacheExpiresAt int64  `json:"-"`
	CacheMAC       string `json:"-"`
	// [FORK] 身份快照随鉴权缓存下发，写日志/用量时不用回查库。
	// 只放名字、不放 DepartmentId：PublishUserAuthCache 用 `*a == *b` 比较两次独立
	// 读取的结果，指针字段会让「值相同」被判成不等，重试耗尽后误报「元数据仍在变」。
	DisplayName    string `json:"display_name"`
	DepartmentName string `json:"department_name"`
}

func (user UserBase) cacheMAC() (string, error) {
	user.Quota = 0
	return authCacheMAC(getUserCacheKey(user.Id), struct {
		User           UserBase
		AuthVersion    int64
		CacheSchema    int
		CacheExpiresAt int64
	}{user, user.AuthVersion, user.CacheSchema, user.CacheExpiresAt})
}

func (user *UserBase) WriteContext(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyUserGroup, user.Group)
	common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
	common.SetContextKey(c, constant.ContextKeyUserStatus, user.Status)
	common.SetContextKey(c, constant.ContextKeyUserEmail, user.Email)
	common.SetContextKey(c, constant.ContextKeyUserName, user.Username)
	common.SetContextKey(c, constant.ContextKeyUserDisplayName, user.DisplayName)
	common.SetContextKey(c, constant.ContextKeyUserDepartmentName, user.DepartmentName)
	common.SetContextKey(c, constant.ContextKeyUserSetting, user.GetSetting())
}

func (user *UserBase) GetSetting() dto.UserSetting {
	setting := dto.UserSetting{}
	if user.Setting != "" {
		err := common.Unmarshal([]byte(user.Setting), &setting)
		if err != nil {
			common.SysLog(common.LogText("failed to unmarshal setting: %s", err.Error()))
		}
	}
	return setting
}

// getUserCacheKey returns the key for user cache
func getUserCacheKey(userId int) string {
	return fmt.Sprintf("user:%d", userId)
}

func userCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// invalidateUserCache clears user cache
func invalidateUserCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	return common.RedisDelKey(getUserCacheKey(userId))
}

// baseUserWithDepartment 构造带部门名的 UserBase：`User` 里只有 DepartmentId，
// 部门名得查字典表，而日志/用量的身份快照直接读这个字段——所以写缓存与冷启动的
// 返回值都必须经过它，绕过一处就会把部门名冲成空串。
//
// 只在写缓存/缓存 miss 时调用，不在请求热路径上。部门已删就留空；查询失败记日志后
// 同样留空：调用点都是「先提交事务再刷缓存」，往上抛等于数据改成功了却告诉客户端失败。
func baseUserWithDepartment(user User) *UserBase {
	base := user.ToBaseUser()
	if user.DepartmentId == nil {
		return base
	}
	var name string
	if err := DB.Model(&Department{}).Where("id = ?", *user.DepartmentId).
		Select("name").Scan(&name).Error; err != nil {
		common.SysLog(common.LogText("failed to resolve department name for user %d: %s", user.Id, err.Error()))
		return base
	}
	base.DepartmentName = name
	return base
}

// populateUserCache 把用户（连部门名）整份写进缓存，用于缓存冷启动灌入。
func populateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return writeUserCache(baseUserWithDepartment(user), true)
}

// updateUserCache refreshes non-quota user cache fields.
// Quota is maintained by atomic quota delta paths and must not be overwritten
// by stale user snapshots from profile/settings updates.
//
// 必须走 baseUserWithDepartment：直接写 ToBaseUser() 会把部门名冲成空串，
// 而这条路径覆盖改密码/改角色/改部门/2FA/passkey 等十几处。
func updateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return writeUserCache(baseUserWithDepartment(user), false)
}

// GetUserCache gets complete user cache from hash
func GetUserCache(userId int) (*UserBase, error) {
	// Try getting from Redis first
	userCache, err := cacheGetUserBase(userId)
	if err == nil {
		return userCache, nil
	}

	// Redis misses and read failures both fall back to the shared database. A
	// version fence newer than the database is the one exception: allowing that
	// snapshot would re-authorize a user while a restrictive update is pending.
	user, err := GetUserById(userId, false)
	if err != nil {
		return nil, err
	}
	// Redis 关闭时没有缓存可写：直接返回 ToBaseUser()。这里不能查部门——那会让每次
	// 鉴权（TokenAuth 等都会调 GetUserCache）多一次查询，且没有缓存可复用结果。
	if !common.RedisEnabled {
		return user.ToBaseUser(), nil
	}
	floor, floorErr := getUserAuthVersionFloor(userId)
	if floorErr == nil && floor > user.AuthVersion {
		return nil, ErrUserAuthCachePending
	}
	if err := populateUserCache(*user); err != nil {
		if errors.Is(err, ErrUserAuthCachePending) {
			return nil, err
		}
		common.SysLog(common.LogText("failed to synchronously populate user cache: %s", err.Error()))
	}
	// 返回带部门名的 base：写进 Redis 的是它，返回值就得是同一份，否则每个缓存周期
	// 的第一条请求都会漏一条没有部门的日志。
	return baseUserWithDepartment(*user), nil
}

func cacheGetUserBase(userId int) (*UserBase, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var userCache UserBase
	// Try getting from Redis first
	err := common.RedisHGetObj(getUserCacheKey(userId), &userCache)
	if err != nil {
		return nil, err
	}
	if userCache.Id != userId || userCache.CacheSchema != userCacheSchemaVersion || userCache.AuthVersion <= 0 {
		return nil, fmt.Errorf("user cache schema is stale")
	}
	mac, err := userCache.cacheMAC()
	if err != nil || !validAuthCacheMAC(mac, userCache.CacheMAC, userCache.CacheExpiresAt) {
		return nil, fmt.Errorf("user cache integrity check failed")
	}
	floor, err := getUserAuthVersionFloor(userId)
	if err != nil {
		return nil, err
	}
	if floor > userCache.AuthVersion {
		return nil, ErrUserAuthCachePending
	}
	return &userCache, nil
}

// Add atomic quota operations using hash fields.
// 通过守卫式 Lua 脚本执行：哈希不存在时直接跳过（下次读取会从数据库水合），
// 不会像裸 HINCRBY 那样创建只含 Quota 字段的残缺哈希。
func cacheIncrUserQuota(userId int, delta int64) error {
	if !common.RedisEnabled {
		return nil
	}
	_, err := cacheApplyUserQuotaDelta(userId, delta)
	return err
}

func cacheDecrUserQuota(userId int, delta int64) error {
	return cacheIncrUserQuota(userId, -delta)
}

// syncCreditUserQuotaCache 在授信事务（充值/兑换等）提交后同步把增量补进缓存
// 余额。预扣以缓存值为准（存在期间），授信不能绕过它，否则新到账的额度在
// 缓存过期前不可用；缓存未命中无需处理，下次读取会从已提交的数据库余额水合。
func syncCreditUserQuotaCache(userId int, quota int, operation string) {
	if quota <= 0 {
		return
	}
	if err := cacheIncrUserQuota(userId, int64(quota)); err != nil {
		common.SysLog(common.LogText("failed to sync %s credit to user quota cache: %s", operation, err.Error()))
	}
}

// Helper functions to get individual fields if needed
func getUserGroupCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Group, nil
}

func getUserQuotaCache(userId int) (int, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return 0, err
	}
	return cache.Quota, nil
}

func getUserNameCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Username, nil
}

func getUserSettingCache(userId int) (dto.UserSetting, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return dto.UserSetting{}, err
	}
	return cache.GetSetting(), nil
}

// RefreshUserGroupCache publishes current user metadata after a group change.
func RefreshUserGroupCache(userId int) error {
	return PublishUserAuthCache(userId)
}

// GetUserLanguage returns the user's language preference from cache
// Uses the existing GetUserCache mechanism for efficiency
func GetUserLanguage(userId int) string {
	userCache, err := GetUserCache(userId)
	if err != nil {
		return ""
	}
	return userCache.GetSetting().Language
}
