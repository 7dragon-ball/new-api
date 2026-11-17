package model

import (
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// [TM-FORK] 部门名归一后为空或超长。message 会被 ApiError 原样渲染进响应体的
// `message` 字段，前端直接展示，所以这句话本身就是给运营看的文案。
var ErrDepartmentNameInvalid = errors.New("Department name must be 1-30 characters (spaces are trimmed)")

// [TM-FORK] 部门字典：单层扁平，一人一部门。
// 显式固定表名为单数 department：GORM 默认会复数化成 departments，而手写 SQL
// （删除计数、部门用量聚合）都写 department。
func (Department) TableName() string { return "department" }

type Department struct {
	Id        int    `json:"id"`
	Name      string `json:"name" gorm:"type:varchar(30);not null;uniqueIndex:uk_department_name"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// [TM-FORK] 写入前归一：唯一索引生效的前提是库里存的已经归一。
// 只在 Save / Create 路径生效；改名必须用 tx.Save(&dept) 而不是 Updates(map)，
// 否则这个赋值写不进 SET 子句，重名检查随即失效。
func (d *Department) BeforeSave(tx *gorm.DB) error {
	d.Name = strings.TrimSpace(strings.ReplaceAll(d.Name, "　", " "))
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > 30 {
		return ErrDepartmentNameInvalid
	}
	return nil
}
