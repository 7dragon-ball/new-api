package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// [FORK] 模型标签字典：`GET /api/models/tags?keyword=&limit=`
//
// 列表页「标签」筛选框的下拉候选。返回去重后的标签全集，按字典序。
// 见 model/model_tag_fork.go 里为什么需要这个接口。
func ListModelTagsFork(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	tags, err := model.ListModelTags(c.Query("keyword"), limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": tags})
}
