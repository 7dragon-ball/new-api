package controller

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// [FORK] GET /api/department/?keyword=
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

// [FORK] POST /api/department/  请求体 {"name": "技术部"}
func CreateDepartment(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
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

// [FORK] PUT /api/department/  请求体 {"id": 1, "name": "技术部"}
// id 走请求体：路径是集合级 PUT，与上游 prefill_group 的形状一致。
func UpdateDepartment(c *gin.Context) {
	var req struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.Id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var dept model.Department
	if err := model.DB.First(&dept, req.Id).Error; err != nil {
		common.ApiError(c, model.ErrDepartmentNotFound)
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

// [FORK] DELETE /api/department/:id
func DeleteDepartment(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	// 部门行锁 + 成员计数 + 删除必须在同一事务里：部门有意不加外键，
	// 无锁时并发的「改归属到本部门」会插在计数与删除之间，留下孤儿归属。
	var deptName string
	var memberCount int64
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		dept, err := model.LockDepartmentWithTx(tx, id)
		if err != nil {
			return err
		}
		if dept == nil {
			return model.ErrDepartmentNotFound
		}
		deptName = dept.Name
		// 只数未软删的用户。外键 RESTRICT 做不到这一点（它看不见 deleted_at），
		// 这正是本方案不用外键的第一条理由。
		if err := tx.Model(&model.User{}).Where("department_id = ?", id).Count(&memberCount).Error; err != nil {
			return err
		}
		if memberCount > 0 {
			return nil
		}
		return tx.Delete(dept).Error
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	if memberCount > 0 {
		common.ApiErrorT(c, "Department still has {{count}} users", map[string]any{"count": memberCount})
		return
	}
	recordManageAudit(c, "department.delete", map[string]any{"id": id, "name": deptName})
	common.ApiSuccess(c, nil)
}

// [FORK] PUT /api/user/:id/department  请求体 {"department_id": 2} 或 {"department_id": null}
// 独立接口而不复用 PUT /api/user/：后者是整行替换语义，上游 UI 不传 department_id 就会把它清空。
// 放在 department_fork.go 而不是 user.go：这是纯新增 handler，同包可拿到 canManageTargetRole
// 与 recordManageAuditFor；user.go 是三组定制共同修改、merge 冲突风险最高的文件。
func UpdateUserDepartment(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	originUser, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiErrorT(c, "User not found")
		return
	}
	// 与 UpdateUser / ManageUser / DeleteUser 同一条边界：同级或更高角色的账号不可操作。
	// 漏掉这一句时，role=10 的管理员能改 role=100 的部门，而它连读该用户都会被拒。
	if !canManageTargetRole(c.GetInt("role"), originUser.Role) {
		common.ApiErrorT(c, "No permission to update users of same or higher permission level")
		return
	}
	// 解成 RawMessage 而不是 *int：*int 下「字段缺失」与「显式 null」都是 nil，
	// 客户端少传或拼错参数名会静默清空部门。清空是操作，必须显式表达。
	var body map[string]common.RawMessage
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	raw, present := body["department_id"]
	if !present {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var departmentID *int
	if common.GetJsonType(raw) != "null" {
		var parsed int
		if err := common.Unmarshal(raw, &parsed); err != nil || parsed <= 0 {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		departmentID = &parsed
	}
	// 改归属与删部门共用部门行锁：锁在读用户之后、写之前，保证并发删部门拿不到
	// 「计数为 0 且随后又有人挂进来」的窗口。
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if departmentID != nil {
			dept, err := model.LockDepartmentWithTx(tx, *departmentID)
			if err != nil {
				return err
			}
			if dept == nil {
				return model.ErrDepartmentNotFound
			}
		}
		// 直接写列，绕开 UpdateWithTx：那里对结构体走 Updates，GORM 会跳过零值，
		// *int 的 nil 正在被跳过之列，清空部门的请求会静默失效（返回 success 但列没变）。
		// 显式 nil 写成 NULL 是这里唯一的目的。
		return tx.Model(&model.User{}).Where("id = ?", id).Update("department_id", departmentID).Error
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	// 审计参数传解引用后的值而不是 *int：auditContentEN 用 %v 渲染模板里的占位符，
	// 传指针会让 Content 写成 "0xc0004a21b0"（清空时是 "<nil>"）。
	var auditDepartmentID any
	if departmentID != nil {
		auditDepartmentID = *departmentID
	}
	recordManageAuditFor(c, id, "user.department_update",
		map[string]any{"id": id, "department_id": auditDepartmentID})
	common.ApiSuccess(c, nil)
}

// [FORK] 唯一索引冲突判定。Postgres 走错误码，SQLite（测试）走错误文本。
func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
