package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// [TM-FORK] GET /api/data/departments?start_timestamp=&end_timestamp=
// 部门池按时间区间聚合用量。三个来源分别聚合成 CTE 再 LEFT JOIN，
// 不能摊平成一条多表 JOIN：users × tokens × quota_data 会做笛卡尔积，
// 有 N 把秘钥的用户调用量会被放大 N 倍（count(DISTINCT ...) 只会掩盖症状）。
// 全部用 LEFT JOIN 是为了让零成员部门也出现在结果里，前端据此先建桶。
func GetDepartmentUsage(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseFlowQuotaTimeRange(c)
	if !ok {
		return
	}

	type row struct {
		Id      int    `json:"id"`
		Name    string `json:"name"`
		Members int64  `json:"members"`
		Keys    int64  `json:"keys"`
		Calls   int64  `json:"calls"`
		Tokens  int64  `json:"tokens"`
		Quota   int64  `json:"quota"`
	}
	var rows []row

	const query = `
WITH dept_users AS (
    SELECT id, department_id FROM users
     WHERE department_id IS NOT NULL AND deleted_at IS NULL
),
tk AS (
    SELECT user_id, count(*) AS keys FROM tokens
     WHERE deleted_at IS NULL
     GROUP BY user_id
),
qd AS (
    SELECT user_id,
           coalesce(sum(count), 0)      AS calls,
           coalesce(sum(token_used), 0) AS tokens,
           coalesce(sum(quota), 0)      AS quota
      FROM quota_data
     WHERE created_at BETWEEN ? AND ?
     GROUP BY user_id
)
SELECT d.id, d.name,
       count(du.id)                AS members,
       coalesce(sum(tk.keys), 0)   AS keys,
       coalesce(sum(qd.calls), 0)  AS calls,
       coalesce(sum(qd.tokens), 0) AS tokens,
       coalesce(sum(qd.quota), 0)  AS quota
  FROM department d
  LEFT JOIN dept_users du ON du.department_id = d.id
  LEFT JOIN tk           ON tk.user_id        = du.id
  LEFT JOIN qd           ON qd.user_id        = du.id
 GROUP BY d.id, d.name
 ORDER BY quota DESC, d.id`

	if err := model.DB.Raw(query, startTimestamp, endTimestamp).Scan(&rows).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}
