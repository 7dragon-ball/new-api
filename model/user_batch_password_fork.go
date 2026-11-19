package model

import "gorm.io/gorm"

// [FORK] 批量重置密码：在调用者的事务里按 id 锁住一行，并回读授权所需的列。
//
// Unscoped 是必需的：软删行也要能被锁住并识别（调用方看 DeletedAt.Valid），
// 否则「行不存在」与「已软删」会混成同一个 gorm.ErrRecordNotFound，
// 也让已软删行的 auth version 无法在锁内推进/回滚。
//
// 调用方必须先调本函数再调 IncrementUserAuthVersionWithTx：前者只加锁不写库，
// 后者会在写库前向 Redis 发布 fail-closed fence。先校验后发 fence，
// 可以把「fence 已发布但事务回滚」的窗口压到最小。
func LockUserForBatchPasswordResetWithTx(tx *gorm.DB, userId int) (*User, error) {
	var user User
	if err := lockForUpdate(tx.Unscoped()).Select("id", "role", "status", "deleted_at").
		Where("id = ?", userId).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}
