package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// [FORK] UpdateAbilityPriority 改一条供应关系（模型 × 渠道）的优先级，不动同渠道的其它行。
func UpdateAbilityPriority(c *gin.Context) {
	var request struct {
		Model     string `json:"model"`
		ChannelId int    `json:"channel_id"`
		Priority  *int64 `json:"priority"`
		Group     string `json:"group"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if request.Model == "" {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Model name cannot be empty"))
		return
	}
	if request.ChannelId <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid channel ID"))
		return
	}
	if request.Priority == nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	err := model.UpdateAbilityPriority(request.Model, request.ChannelId, request.Group, *request.Priority)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrAbilityPriorityRange):
			common.ApiErrorStatus(c, http.StatusBadRequest, err)
		case errors.Is(err, model.ErrAbilityPriorityNotFound),
			errors.Is(err, model.ErrAbilityChannelNotFound),
			errors.Is(err, model.ErrAbilityGroupNotFound):
			common.ApiErrorStatus(c, http.StatusNotFound, err)
		default:
			common.ApiError(c, err)
		}
		return
	}
	// [FORK] 写后立即刷新缓存，不必等 3 秒 sync；与 ReorderChannelPriority 对齐。
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"model":      request.Model,
		"channel_id": request.ChannelId,
		"priority":   *request.Priority,
	}})
}
