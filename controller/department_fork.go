package controller

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

// [TM-FORK] GET /api/department/?keyword=
// 无 keyword 时返回全量（部门是几十级的小字典，无需分页）。
func GetDepartments(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	var departments []*model.Department
	query := model.DB.Model(&model.Department{}).Order("name")
	// 直接 LIKE 会让入参里的 % / _ 当通配符用，搜 "%" 会静默命中全部；
	// 转义 + ESCAPE 让它保持字面量语义（PG 与 SQLite 都支持这个写法）。
	if keyword != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
		query = query.Where(`name LIKE ? ESCAPE '\'`, "%"+escaped+"%")
	}
	if err := query.Find(&departments).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, departments)
}

// [TM-FORK] POST /api/department/  请求体 {"name": "技术部"}
func CreateDepartment(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	dept := model.Department{Name: req.Name}
	// Save 才会触发 BeforeSave（归一 + 长度校验），和 UpdateDepartment 走同一条路径。
	if err := model.DB.Save(&dept).Error; err != nil {
		if isDuplicateKey(err) {
			common.ApiErrorT(c, "Department name already exists")
			return
		}
		common.ApiError(c, err) // 含 ErrDepartmentNameInvalid 的原文
		return
	}
	recordManageAudit(c, "department.create", map[string]any{"id": dept.Id, "name": dept.Name})
	common.ApiSuccess(c, &dept)
}

// [TM-FORK] PUT /api/department/  请求体 {"id": 1, "name": "技术部"}
// id 走请求体：路径是集合级 PUT，与上游 prefill_group 的形状一致。
func UpdateDepartment(c *gin.Context) {
	var req struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var dept model.Department
	if err := model.DB.First(&dept, req.Id).Error; err != nil {
		common.ApiErrorT(c, "Department not found")
		return
	}
	dept.Name = req.Name
	// 必须用 Save 而不是 Updates(map)：只有 Save 会走 BeforeSave 把归一后的名字
	// 写进 SET 子句；用 Updates 时「技术部␣」能绕过唯一索引。
	if err := model.DB.Save(&dept).Error; err != nil {
		if isDuplicateKey(err) {
			common.ApiErrorT(c, "Department name already exists")
			return
		}
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "department.update", map[string]any{"id": dept.Id, "name": dept.Name})
	common.ApiSuccess(c, &dept)
}

// [TM-FORK] DELETE /api/department/:id
func DeleteDepartment(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var dept model.Department
	if err := model.DB.First(&dept, id).Error; err != nil {
		common.ApiErrorT(c, "Department not found")
		return
	}
	// 只数未软删的用户。外键 RESTRICT 做不到这一点（它看不见 deleted_at），
	// 这正是本方案不用外键的第一条理由。
	var count int64
	if err := model.DB.Model(&model.User{}).Where("department_id = ?", id).Count(&count).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	if count > 0 {
		common.ApiErrorT(c, "Department still has {{count}} users", map[string]any{"count": count})
		return
	}
	if err := model.DB.Delete(&dept).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "department.delete", map[string]any{"id": id, "name": dept.Name})
	common.ApiSuccess(c, nil)
}

// [TM-FORK] 唯一索引冲突判定。Postgres 走错误码，SQLite（测试）走错误文本。
func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
