package controller

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// [FORK] POST /api/user/batch/reset_password
//
// 一次 step-up proof 覆盖一批账号：为每个目标生成各自独立的随机密码并在响应里
// 返回一次。全或无——任一目标不可重置则整批回滚、不返回任何密码。
// 不删除 user_access_tokens：与现有三条改密路径（PUT /api/user/、PUT /api/user/self、
// POST /api/user/reset）一致，代价见 fork/README.md「批量重置用户密码」。

// 事务内二次授权失败用哨兵错误回传，事务外再映射成对调用方稳定可见的消息 key。
var (
	errBatchPasswordTargetMissing   = errors.New("batch password reset: target user no longer exists")
	errBatchPasswordTargetDisabled  = errors.New("batch password reset: target user is disabled")
	errBatchPasswordTargetForbidden = errors.New("batch password reset: target user is not manageable")
)

type batchPasswordResetRequest struct {
	Ids []int `json:"ids"`
}

type batchPasswordResetUser struct {
	Id          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

func BatchResetUserPasswords(c *gin.Context) {
	var request batchPasswordResetRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	// ids 的形状校验必须在消费 proof 之前：它不依赖授权结果，而且能省掉
	// 一次「proof 已被消费、请求却因参数错误被拒」的糟糕体验。
	sortedIDs, ok := service.NormalizeBatchPasswordResetUserIDs(request.Ids)
	if !ok {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	myRole := c.GetInt("role")

	// 先把所有可拒绝条件在事务外查完：这一步只用于尽早拒绝，不构成授权保证
	//（真正的授权在事务内加锁后重做一次）。
	targets := make(map[int]*model.User, len(sortedIDs))
	for _, id := range sortedIDs {
		user, err := model.GetUserById(id, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				common.ApiErrorT(c, "User does not exist")
				return
			}
			common.ApiError(c, err)
			return
		}
		if user.Status != common.UserStatusEnabled {
			common.ApiErrorT(c, "This user account is disabled.")
			return
		}
		if !canManageTargetRole(myRole, user.Role) {
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
			return
		}
		targets[id] = user
	}

	authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserPasswordBatchReset,
		service.AdminUserPasswordBatchResetContext{UserIDs: sortedIDs})
	if authorization == nil {
		return
	}

	// Argon2id 每个目标 19 MiB，全部在事务外算完，不占着行锁。
	plainPasswords := make(map[int]string, len(sortedIDs))
	passwordHashes := make(map[int]string, len(sortedIDs))
	for _, id := range sortedIDs {
		password, err := common.GenerateRandomAccountPassword()
		if err != nil {
			common.ApiError(c, err)
			return
		}
		hash, err := common.HashAccountPassword(password)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		plainPasswords[id] = password
		passwordHashes[id] = hash
	}

	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		return resetBatchPasswordsInTx(tx, myRole, sortedIDs, passwordHashes)
	}); err != nil {
		switch {
		case errors.Is(err, errBatchPasswordTargetMissing):
			common.ApiErrorT(c, "User does not exist")
		case errors.Is(err, errBatchPasswordTargetDisabled):
			common.ApiErrorT(c, "This user account is disabled.")
		case errors.Is(err, errBatchPasswordTargetForbidden):
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
		default:
			common.ApiError(c, err)
		}
		return
	}

	// 事务已提交。以下两步是缓存与记录清理，失败只记日志、不影响响应：
	// auth version 已递增，旧浏览器会话在鉴权时已被拒绝；若因为清理失败而
	// 丢弃密码，用户会被永久锁在门外。
	for _, id := range sortedIDs {
		if err := model.PublishUserAuthCache(id); err != nil {
			logger.LogWarn(c.Request.Context(), common.LogText("failed to publish auth cache for user %d after batch password reset: %s", id, err.Error()))
		}
		if _, err := model.RevokeAllUserSessions(id, "admin_batch_password_reset"); err != nil {
			logger.LogWarn(c.Request.Context(), common.LogText("failed to revoke sessions for user %d after batch password reset: %s", id, err.Error()))
		}
	}

	// 响应按请求的原始顺序重排；DB 操作与审计用升序集合。
	users := make([]batchPasswordResetUser, 0, len(request.Ids))
	for _, id := range request.Ids {
		target := targets[id]
		users = append(users, batchPasswordResetUser{
			Id:          target.Id,
			Username:    target.Username,
			DisplayName: target.DisplayName,
			Email:       target.Email,
			Password:    plainPasswords[id],
		})
	}

	// 审计绝不带密码；recordManageAudit 会 markAuditLogged，跳过中间件兜底，
	// 不会重复记一条 generic。
	recordManageAudit(c, "user.batch_password_reset", map[string]any{
		"count":               len(sortedIDs),
		"user_ids":            sortedIDs,
		"verification_method": authorization.Method,
	})
	common.ApiSuccess(c, gin.H{"users": users})
}

// resetBatchPasswordsInTx 在调用方的事务里逐个加锁并重置密码。
//
// 按 ids 升序加锁：固定加锁顺序，避免两个并发批量请求因顺序不同死锁。
// 锁内重读 role / status / deleted_at 并再次授权：事务外那次预检只用于尽早拒绝，
// 到此刻之间目标可能已被提权、被禁用或被删除，所以它不构成授权保证。
func resetBatchPasswordsInTx(tx *gorm.DB, myRole int, ids []int, passwordHashes map[int]string) error {
	for _, id := range ids {
		locked, err := model.LockUserForBatchPasswordResetWithTx(tx, id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errBatchPasswordTargetMissing
			}
			return err
		}
		if locked.DeletedAt.Valid {
			return errBatchPasswordTargetMissing
		}
		if locked.Status != common.UserStatusEnabled {
			return errBatchPasswordTargetDisabled
		}
		if !canManageTargetRole(myRole, locked.Role) {
			return errBatchPasswordTargetForbidden
		}
		// 顺序与 model.ResetUserPasswordByEmail 一致：先推进 auth version
		//（自带行锁 + Redis fail-closed fence），再写新散列。
		if _, err := model.IncrementUserAuthVersionWithTx(tx, id); err != nil {
			return err
		}
		result := tx.Model(&model.User{}).Where("id = ?", id).Update("password", passwordHashes[id])
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errBatchPasswordTargetMissing
		}
	}
	return nil
}
