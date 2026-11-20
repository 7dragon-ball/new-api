package model

import "gorm.io/gorm"

// [FORK] 批量重置密码：在调用者的事务里按 id 锁住一行，并回读授权与回显所需的列。
//
// Unscoped 是必需的：软删行也要能被锁住并识别（调用方看 DeletedAt.Valid），
// 否则「行不存在」与「已软删」会混成同一个 gorm.ErrRecordNotFound，
// 也让已软删行的 auth version 无法在锁内推进/回滚。
//
// username / display_name / email 一并读出来，是因为响应要用它们渲染；事务外那次
// 预检的快照到提交之间可能已被改名或改邮箱，回显旧值会让导出的 CSV 与列表对不上。
//
// 调用方必须先调本函数再调 IncrementUserAuthVersionWithTx：前者只加锁不写库，
// 后者会在写库前向 Redis 发布 fail-closed fence。先校验后发 fence，
// 可以把「fence 已发布但事务回滚」的窗口压到最小。
func LockUserForBatchPasswordResetWithTx(tx *gorm.DB, userId int) (*User, error) {
	var user User
	if err := lockForUpdate(tx.Unscoped()).
		Select("id", "role", "status", "deleted_at", "username", "display_name", "email").
		Where("id = ?", userId).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}
