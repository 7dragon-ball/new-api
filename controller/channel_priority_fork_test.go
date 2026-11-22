package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 按供应关系设优先级的端点：缺省 group 落 default，写后刷新缓存。
func TestUpdateAbilityPriorityEndpoint(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}))
	channel := &model.Channel{Name: "supply-priority", Key: "test-key"}
	require.NoError(t, db.Create(channel).Error)
	other := &model.Channel{Name: "supply-priority-other", Key: "test-key"}
	require.NoError(t, db.Create(other).Error)
	seven := int64(7)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "priority-model", ChannelId: channel.Id, Enabled: true, Priority: &seven},
		{Group: "default", Model: "other-model", ChannelId: channel.Id, Enabled: true, Priority: &seven},
		{Group: "default", Model: "priority-model", ChannelId: other.Id, Enabled: true, Priority: &seven},
	}).Error)

	call := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/ability/priority", strings.NewReader(body))
		UpdateAbilityPriority(ctx)
		return recorder
	}
	priorityOf := func(modelName string, channelId int) int64 {
		var ability model.Ability
		require.NoError(t, db.Where("`group` = ? AND model = ? AND channel_id = ?", "default", modelName, channelId).First(&ability).Error)
		require.NotNil(t, ability.Priority)
		return *ability.Priority
	}

	recorder := call(fmt.Sprintf(`{"model":"priority-model","channel_id":%d,"priority":3}`, channel.Id))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Model     string `json:"model"`
			ChannelId int    `json:"channel_id"`
			Priority  int64  `json:"priority"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, "priority-model", response.Data.Model)
	assert.Equal(t, channel.Id, response.Data.ChannelId)
	assert.Equal(t, int64(3), response.Data.Priority)
	assert.Equal(t, int64(3), priorityOf("priority-model", channel.Id), "an omitted group hits the default group")
	assert.Equal(t, int64(7), priorityOf("other-model", channel.Id))
	assert.Equal(t, int64(7), priorityOf("priority-model", other.Id))

	for _, body := range []string{
		`{"channel_id":1,"priority":3}`,
		`{"model":"priority-model","priority":3}`,
		`{"model":"priority-model","channel_id":0,"priority":3}`,
		`{"model":"priority-model","channel_id":1}`,
		`{"model":"priority-model","channel_id":1,"priority":0}`,
		`{"model":"priority-model","channel_id":1,"priority":1000}`,
		`{"model":"priority-model","channel_id":1,"priority":3.5}`,
	} {
		assert.Equal(t, http.StatusBadRequest, call(body).Code, body)
	}

	missing := call(fmt.Sprintf(`{"model":"missing-model","channel_id":%d,"priority":3}`, channel.Id))
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), `"message_key":"The supply relation does not exist"`)
	outOfRange := call(fmt.Sprintf(`{"model":"priority-model","channel_id":%d,"priority":1000}`, channel.Id))
	assert.Equal(t, http.StatusBadRequest, outOfRange.Code)
	assert.Contains(t, outOfRange.Body.String(), `"message_key":"Priority must be between 1 and 999"`)
}

// [FORK] 写后立即生效：不等 3 秒 sync，端点返回后下一次选路就按新优先级走。
func TestUpdateAbilityPriorityRefreshesRoutingCache(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	previousMemoryCache := common.MemoryCacheEnabled
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	common.MemoryCacheEnabled = true

	const modelName = "refresh-model"
	high, low, weight := int64(7), int64(3), uint(1)
	preferred := &model.Channel{Name: "refresh-preferred", Key: "test-key", Models: modelName, Group: "default",
		Status: common.ChannelStatusEnabled, Priority: &high, Weight: &weight}
	require.NoError(t, db.Create(preferred).Error)
	fallback := &model.Channel{Name: "refresh-fallback", Key: "test-key", Models: modelName, Group: "default",
		Status: common.ChannelStatusEnabled, Priority: &low, Weight: &weight}
	require.NoError(t, db.Create(fallback).Error)
	seven, three := int64(7), int64(3)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: modelName, ChannelId: preferred.Id, Enabled: true, Priority: &seven, Weight: 1},
		{Group: "default", Model: modelName, ChannelId: fallback.Id, Enabled: true, Priority: &three, Weight: 1},
	}).Error)
	model.InitChannelCache()

	routed := func() int {
		channel, err := model.GetRandomSatisfiedChannel("default", modelName, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, channel)
		return channel.Id
	}
	require.Equal(t, preferred.Id, routed())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/ability/priority",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"channel_id":%d,"priority":9}`, modelName, fallback.Id)))
	UpdateAbilityPriority(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	assert.Equal(t, fallback.Id, routed(), "端点返回后缓存已刷新，选路按新优先级")
}
