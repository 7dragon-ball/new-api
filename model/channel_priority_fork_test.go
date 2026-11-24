package model

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	// group 不在 channels.group 里时与 Add/Delete 同口径报「渠道不提供该分组」。
	assert.ErrorIs(t, UpdateAbilityPriority("model-x", channelA.Id, "missing-group", 500), ErrAbilityGroupNotFound)
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

// [FORK] 单条优先级写入与 abilities 重建串行化：重建是「读旧 priority → 删 → 插」的
// 读改写，并发写入若落在读与删之间，会被重建用读到的旧值覆盖（接口仍返回成功），
// 或让写入影响 0 行、误报 404。两条路径现在都先锁渠道行，这里在重建读到旧值后触发
// 一次并发写入，断言最终值不被回退。
//
// 只在 PostgreSQL 上跑：SQLite 没有 FOR UPDATE（lockForUpdate 跳过该子句），复现不出
// 这个交错；MySQL 本地没有实例。用法与其它多方言用例一致：
//
//	TEST_TASK_DB_DIALECT=postgres TEST_POSTGRES_DSN=... go test ./model -run SerializesWithRebuild
func TestUpdateAbilityPrioritySerializesWithRebuild(t *testing.T) {
	if common.DatabaseType(os.Getenv("TEST_TASK_DB_DIALECT")) != common.DatabaseTypePostgreSQL {
		t.Skip("requires TEST_TASK_DB_DIALECT=postgres and TEST_POSTGRES_DSN")
	}
	useScheduledChannelDatabase(t)
	channelPriority, weight := int64(3), uint(1)
	channel := &Channel{
		Name:     "priority-race",
		Key:      "test-only",
		Models:   "race-model",
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: &channelPriority,
		Weight:   &weight,
	}
	require.NoError(t, channel.Insert())
	priorityOf := func() int64 {
		var row Ability
		require.NoError(t, DB.Where(commonGroupCol+" = ? AND model = ? AND channel_id = ?", "default", "race-model", channel.Id).First(&row).Error)
		require.NotNil(t, row.Priority)
		return *row.Priority
	}
	require.Equal(t, int64(3), priorityOf())

	abilitiesTable := DB.NamingStrategy.TableName("abilities")
	written := make(chan error, 1)
	var wroteEarly bool
	var earlyErr error
	triggered := false
	// 回调在重建读完旧 priority 之后、删除之前触发一次并发写入。
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("fork_priority_race", func(tx *gorm.DB) {
		if triggered || tx.Statement.Table != abilitiesTable {
			return
		}
		triggered = true
		go func() {
			written <- UpdateAbilityPriority("race-model", channel.Id, "default", 9)
		}()
		// 未加锁时并发写入会在窗口内提交（随后被旧值覆盖）；加锁后它阻塞到重建提交。
		select {
		case earlyErr = <-written:
			wroteEarly = true
		case <-time.After(500 * time.Millisecond):
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, DB.Callback().Query().Remove("fork_priority_race"))
	})

	require.NoError(t, channel.UpdateAbilities(nil))
	if wroteEarly {
		require.NoError(t, earlyErr)
	} else {
		select {
		case err := <-written:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("the concurrent priority write never returned")
		}
	}
	assert.Equal(t, int64(9), priorityOf(), "重建不能用读到的旧值覆盖并发的单条优先级写入")
}
