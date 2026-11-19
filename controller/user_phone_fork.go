package controller

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

var phoneForkRe = regexp.MustCompile(`^[0-9]{11}$`)

// 字符集必须与 token-master2 的 btrim 逐字一致：btrim 默认只去 ASCII 空格，
// 制表符/换行/全角空格(U+3000)/NBSP(U+00A0) 都要显式列出。
const phoneForkTrimChars = " \t\r\n　 "

var ErrInvalidPhoneFork = common.NewMessage("Phone number must be 11 digits")
var ErrPhoneTakenFork = common.NewMessage("Phone number is already used by another user")

// normalizePhoneFork 归一建号请求里的手机号。
// 返回 nil 表示「没有手机号」——空串与纯空白都归一成 nil，不是 ''（空串会占唯一索引）。
func normalizePhoneFork(raw string) (*string, error) {
	trimmed := strings.Trim(raw, phoneForkTrimChars)
	if trimmed == "" {
		return nil, nil
	}
	if !phoneForkRe.MatchString(trimmed) {
		return nil, ErrInvalidPhoneFork
	}
	return &trimmed, nil
}

// ensurePhoneFreeWithTx 在事务内预检号码占用。调用方必须已在事务里。
//
// 预检不能替代唯一索引：并发窗口只能由约束兜住。它的作用是让「重复」在 SQLite
// 测试库上也可测（测试库不走 PG 的约束迁移路径），并给出可读的 PHONE_TAKEN。
//
// Unscoped() 是必需的：软删用户仍占着号码（部分唯一索引不过滤 deleted_at），
// 预检必须与索引口径一致，否则会出现「预检放行、写入撞索引」。
func ensurePhoneFreeWithTx(tx *gorm.DB, phone string) error {
	var count int64
	if err := tx.Model(&model.User{}).Unscoped().Where("phone = ?", phone).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrPhoneTakenFork
	}
	return nil
}
