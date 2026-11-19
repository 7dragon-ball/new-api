package model

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// [FORK] 手机号唯一索引冲突判定。
//
// 只认 uk_users_phone：users 表上还有其它唯一索引（username、aff_code、
// access_token），把任意 23505 都当成「手机号被占用」会把重名报错说成号码冲突。
// Postgres 用错误码 + 约束名；SQLite（测试库）用错误文本，它没有约束名可读。
func IsPhoneDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" && pgErr.ConstraintName == "uk_users_phone"
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: users.phone")
}
