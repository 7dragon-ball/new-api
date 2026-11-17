package model

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// [FORK] 部门名归一后为空或超长。用 common.NewMessage 而不是 errors.New：这句话
// 既是响应体的 message，也是前端 locale 文件里的 key（message_key），非英文界面
// 才翻译得出来；与本文件的 ErrDepartmentNotFound 同一种写法。
var ErrDepartmentNameInvalid = common.NewMessage("Department name must be 1-30 characters (spaces are trimmed)")

// [FORK] 部门不存在。建号、改归属、删部门三条路径共用这一个错误值。
// 用 common.NewMessage：它会经 common.ApiError 一并带上 message_key，
// 与 model/user.go 的 ErrLastEnabledRootUser 同一写法（也保留了原先 ApiErrorT 的响应形状）。
var ErrDepartmentNotFound = common.NewMessage("Department not found")

// [FORK] 锁定部门行并返回它（不存在时返回 nil）。改归属与删部门都必须先拿这把锁：
// 部门有意不加外键，没有数据库层的引用保护，「数成员 → 删除」与「改归属 → 提交」
// 在无锁时会交错，留下指向已删部门的孤儿归属。调用方必须在事务内调用。
// SQLite 不支持 FOR UPDATE（lockForUpdate 会跳过），由它的单写者模型兜底。
func LockDepartmentWithTx(tx *gorm.DB, id int) (*Department, error) {
	var departments []*Department
	if err := lockForUpdate(tx.Model(&Department{})).Where("id = ?", id).Limit(1).Find(&departments).Error; err != nil {
		return nil, err
	}
	if len(departments) == 0 {
		return nil, nil
	}
	return departments[0], nil
}

// [FORK] 建号与改归属共用的部门存在性校验。走锁而不是 COUNT，让「校验通过」
// 与「本事务提交」之间不会被执行中的删部门插入。调用方必须在事务内调用。
func EnsureDepartmentExistsWithTx(tx *gorm.DB, id int) error {
	dept, err := LockDepartmentWithTx(tx, id)
	if err != nil {
		return err
	}
	if dept == nil {
		return ErrDepartmentNotFound
	}
	return nil
}

// [FORK] 部门字典：单层扁平，一人一部门。
// 显式固定表名为单数 department：GORM 默认会复数化成 departments，而手写 SQL
// （删除计数、部门用量聚合）都写 department。
func (Department) TableName() string { return "department" }

type Department struct {
	Id        int    `json:"id"`
	Name      string `json:"name" gorm:"type:varchar(30);not null;uniqueIndex:uk_department_name"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// [FORK] 写入前归一：唯一索引生效的前提是库里存的已经归一。
// 只在 Save / Create 路径生效；改名必须用 tx.Save(&dept) 而不是 Updates(map)，
// 否则这个赋值写不进 SET 子句，重名检查随即失效。
func (d *Department) BeforeSave(tx *gorm.DB) error {
	d.Name = strings.TrimSpace(strings.ReplaceAll(d.Name, "　", " "))
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > 30 {
		return ErrDepartmentNameInvalid
	}
	return nil
}
