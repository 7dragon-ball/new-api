package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 新增供应关系：channels.models 是权威，abilities 是重建产物。
func TestAddAbilityWritesChannelModelsAndAbilities(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{
		Name:     "ability-add",
		Key:      "test-key",
		Models:   "existing-model",
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Weight:   common.GetPointer(uint(3)),
		Priority: common.GetPointer(int64(7)),
	}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))

	require.NoError(t, AddAbility("new-model", channel.Id, "", "", 5))

	stored, err := GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.Contains(t, stored.GetModels(), "existing-model")
	assert.Contains(t, stored.GetModels(), "new-model", "the model name must land in channels.models")

	var row Ability
	require.NoError(t, DB.Where(commonGroupCol+" = ? AND model = ? AND channel_id = ?", "default", "new-model", channel.Id).First(&row).Error)
	require.NotNil(t, row.Priority)
	assert.Equal(t, int64(5), *row.Priority, "the requested priority wins over channels.priority")
	assert.Equal(t, uint(3), row.Weight, "weight follows the channel")
	assert.True(t, row.Enabled)

	// The pre-existing relation keeps the channel priority (UpdateAbilities 的保留语义).
	// GORM 的 First 会把 dest 里已填的主键一起带进 WHERE，这里必须换一个新变量。
	var kept Ability
	require.NoError(t, DB.Where(commonGroupCol+" = ? AND model = ? AND channel_id = ?", "default", "existing-model", channel.Id).First(&kept).Error)
	require.NotNil(t, kept.Priority)
	assert.Equal(t, int64(7), *kept.Priority)
}

func TestAddAbilityRejectsDuplicateExistingRelation(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-dup", Key: "k", Models: "dup-model", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))

	assert.ErrorIs(t, AddAbility("dup-model", channel.Id, "default", "", 1), ErrAbilityAlreadyExists)
}

func TestAddAbilityWritesModelMapping(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-map", Key: "k", Models: "upstream-a", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)

	require.NoError(t, AddAbility("platform-model", channel.Id, "default", "upstream-a", 1))

	stored, err := GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.Contains(t, stored.GetModels(), "platform-model")
	var mapping map[string]string
	require.NoError(t, common.UnmarshalJsonStr(stored.GetModelMapping(), &mapping))
	assert.Equal(t, "upstream-a", mapping["platform-model"])
}

func TestAddAbilityKeepsExistingMappingWhenNoUpstreamGiven(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{
		Name: "ability-keep-map", Key: "k", Models: "other", Group: "default",
		Status: common.ChannelStatusEnabled, ModelMapping: common.GetPointer(`{"other":"real-other"}`),
	}
	require.NoError(t, DB.Create(channel).Error)

	require.NoError(t, AddAbility("plain-model", channel.Id, "default", "", 2))

	stored, err := GetChannelById(channel.Id, false)
	require.NoError(t, err)
	var mapping map[string]string
	require.NoError(t, common.UnmarshalJsonStr(stored.GetModelMapping(), &mapping))
	assert.Equal(t, map[string]string{"other": "real-other"}, mapping, "an untouched mapping must survive")
}

func TestAddAbilityRejectsMissingChannelAndGroup(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-group", Key: "k", Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)

	assert.ErrorIs(t, AddAbility("m", 999999, "default", "", 1), ErrAbilityChannelNotFound)
	assert.ErrorIs(t, AddAbility("m", channel.Id, "vip", "", 1), ErrAbilityGroupNotFound)
	assert.ErrorIs(t, AddAbility("m", channel.Id, "default", "", 0), ErrAbilityPriorityRange)
	assert.ErrorIs(t, AddAbility("m", channel.Id, "default", "", 1000), ErrAbilityPriorityRange)
}

// [FORK] 删一条供应关系：模型名从 channels.models 里摘掉，abilities 行随之消失。
func TestDeleteAbilityRemovesModelFromChannel(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{
		Name: "ability-del", Key: "k", Models: "keep-me,drop-me", Group: "default",
		Status: common.ChannelStatusEnabled, ModelMapping: common.GetPointer(`{"drop-me":"real-drop"}`),
	}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))

	require.NoError(t, DeleteAbility("drop-me", channel.Id, ""))

	stored, err := GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep-me"}, stored.GetModels())
	// 映射清空后写法是空串（与渠道接入清空 model_mapping 的口径一致），不是 "{}"
	assert.Empty(t, stored.GetModelMapping(), "the mapping of a removed model must go too")

	var count int64
	require.NoError(t, DB.Model(&Ability{}).Where("model = ? AND channel_id = ?", "drop-me", channel.Id).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, DB.Model(&Ability{}).Where("model = ? AND channel_id = ?", "keep-me", channel.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestDeleteAbilityRejectsMissingRelation(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-del-missing", Key: "k", Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))

	assert.ErrorIs(t, DeleteAbility("other", channel.Id, "default"), ErrAbilityPriorityNotFound)
	assert.ErrorIs(t, DeleteAbility("m", 999999, "default"), ErrAbilityChannelNotFound)
}

// [FORK] 写后选路立即看到新供应关系（缓存路径的候选来自 channels.models）。
func TestAddAbilityIsRoutableAfterCacheRefresh(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-route", Key: "k", Models: "landing-model", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	InitChannelCache()

	picked, err := GetRandomSatisfiedChannel("default", "brand-new-model", 0, nil)
	require.NoError(t, err)
	assert.Nil(t, picked, "the channel does not supply the model yet")

	require.NoError(t, AddAbility("brand-new-model", channel.Id, "default", "", 1))
	InitChannelCache()

	picked, err = GetRandomSatisfiedChannel("default", "brand-new-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, picked)
	assert.Equal(t, channel.Id, picked.Id)
}

func TestAddAbilityBumpsChannelsRevision(t *testing.T) {
	useScheduledChannelDatabase(t)
	channel := &Channel{Name: "ability-bump", Key: "k", Models: "bump-base", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(channel).Error)

	before := channelsRevision(t)
	require.NoError(t, AddAbility("bump-new", channel.Id, "default", "", 1))
	assert.NotEqual(t, before, channelsRevision(t))
}
