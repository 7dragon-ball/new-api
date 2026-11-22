package model

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 按供应关系设优先级写接口：只改一条 (group, model, channel)，其它行不动。
func TestUpdateAbilityPriorityKeepsOtherRows(t *testing.T) {
	useScheduledChannelDatabase(t)
	var channelA, channelB Channel
	require.NoError(t, DB.Create(&channelA).Error)
	require.NoError(t, DB.Create(&channelB).Error)
	p1, p2, p3 := int64(7), int64(7), int64(7)
	rows := []Ability{
		{Group: "default", Model: "model-x", ChannelId: channelA.Id, Enabled: true, Priority: &p1},
		{Group: "default", Model: "model-y", ChannelId: channelA.Id, Enabled: true, Priority: &p2},
		{Group: "default", Model: "model-x", ChannelId: channelB.Id, Enabled: true, Priority: &p3},
		{Group: "vip", Model: "model-x", ChannelId: channelA.Id, Enabled: true, Priority: &p1},
	}
	require.NoError(t, DB.Create(&rows).Error)

	require.NoError(t, UpdateAbilityPriority("model-x", channelA.Id, "", 3))

	var stored []Ability
	require.NoError(t, DB.Order("channel_id, model, "+commonGroupCol).Find(&stored).Error)
	got := make(map[string]int64, len(stored))
	for _, row := range stored {
		require.NotNil(t, row.Priority)
		got[row.Group+"/"+row.Model+"/"+strconv.Itoa(row.ChannelId)] = *row.Priority
	}
	assert.Equal(t, map[string]int64{
		"default/model-x/" + strconv.Itoa(channelA.Id): 3,
		"default/model-y/" + strconv.Itoa(channelA.Id): 7,
		"default/model-x/" + strconv.Itoa(channelB.Id): 7,
		"vip/model-x/" + strconv.Itoa(channelA.Id):     7,
	}, got, "only the (default, model-x, channel A) row changes")

	// Equal priorities of different supply relations are allowed.
	require.NoError(t, UpdateAbilityPriority("model-x", channelB.Id, "default", 3))
	var duplicate int64
	require.NoError(t, DB.Model(&Ability{}).Where(commonGroupCol+" = ? AND model = ? AND priority = ?", "default", "model-x", 3).Count(&duplicate).Error)
	assert.Equal(t, int64(2), duplicate)

	for _, priority := range []int64{0, 1000, -1} {
		assert.ErrorIs(t, UpdateAbilityPriority("model-x", channelA.Id, "default", priority), ErrAbilityPriorityRange, "priority %d is out of range", priority)
	}
	// The range check runs before the row lookup.
	assert.ErrorIs(t, UpdateAbilityPriority("missing-model", channelA.Id, "default", 500), ErrAbilityPriorityNotFound)
	assert.ErrorIs(t, UpdateAbilityPriority("model-x", channelA.Id, "missing-group", 500), ErrAbilityPriorityNotFound)
}

func TestUpdateAbilityPriorityBumpsChannels(t *testing.T) {
	useScheduledChannelDatabase(t)
	var channel Channel
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "bump-model", ChannelId: channel.Id, Enabled: true}).Error)

	before := channelsRevision(t)
	require.NoError(t, UpdateAbilityPriority("bump-model", channel.Id, "default", 2))
	assert.NotEqual(t, before, channelsRevision(t), "the gateway cache is refreshed")

	// Writing the same value again still reports success.
	require.NoError(t, UpdateAbilityPriority("bump-model", channel.Id, "default", 2))
}
