package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// [FORK] 模型上架页「支持渠道与模型」的写入口：新增一条供应关系（平台模型 × 渠道）。
// body: {group?, model, channel_id, upstream_model?, priority?}
func AddChannelAbility(c *gin.Context) {
	var request struct {
		Model         string `json:"model"`
		ChannelId     int    `json:"channel_id"`
		Group         string `json:"group"`
		UpstreamModel string `json:"upstream_model"`
		Priority      *int64 `json:"priority"`
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
	// 缺省 1，与 PRD「优先级默认值 1」一致
	priority := int64(1)
	if request.Priority != nil {
		priority = *request.Priority
	}
	err := model.AddAbility(request.Model, request.ChannelId, request.Group, request.UpstreamModel, priority)
	if err != nil {
		abilityWriteError(c, err)
		return
	}
	// [FORK] 写后立即刷新缓存，不必等 3 秒 sync；与优先级写接口一致。
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"model":          request.Model,
		"channel_id":     request.ChannelId,
		"upstream_model": request.UpstreamModel,
		"priority":       priority,
	}})
}

// [FORK] 删除一条供应关系。body: {group?, model, channel_id}
func DeleteChannelAbility(c *gin.Context) {
	var request struct {
		Model     string `json:"model"`
		ChannelId int    `json:"channel_id"`
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
	err := model.DeleteAbility(request.Model, request.ChannelId, request.Group)
	if err != nil {
		abilityWriteError(c, err)
		return
	}
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"model":      request.Model,
		"channel_id": request.ChannelId,
	}})
}

// [FORK] 两个写接口共用的错误映射：优先级越界与「已存在」是 400，
// 目标不存在（渠道 / 分组 / 供应关系）是 404，其余按上游惯例回 200 + success:false。
func abilityWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrAbilityPriorityRange),
		errors.Is(err, model.ErrAbilityAlreadyExists),
		errors.Is(err, model.ErrAbilityMappingInvalid):
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
	case errors.Is(err, model.ErrAbilityPriorityNotFound),
		errors.Is(err, model.ErrAbilityChannelNotFound),
		errors.Is(err, model.ErrAbilityGroupNotFound):
		common.ApiErrorStatus(c, http.StatusNotFound, err)
	default:
		common.ApiError(c, err)
	}
}
