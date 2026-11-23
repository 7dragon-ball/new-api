package model

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// [FORK] 选路优先级基线：改造前后必须一致。基准是「改造前的缓存路径」，
// 不是 GetChannel（后者用 ability.Weight+10 平滑，weight 口径本来就不同）。
// 断言用返回 ID 的集合而不是具体 id —— 同层按 weight 随机分流。
func TestRoutingPriorityBaseline(t *testing.T) {
	useScheduledChannelDatabase(t)
	const modelName = "baseline-model"
	idsByPriority := make(map[int64][]int)
	// priority 5 / 3 / 1 / 1，各 weight 1；channel.Insert() 会把 channel.Priority 抄进 abilities
	for i, p := range []int64{5, 3, 1, 1} {
		priority := p
		weight := uint(1)
		channel := &Channel{
			Name:     fmt.Sprintf("baseline-c%d-%d", p, i),
			Key:      "test-only",
			Models:   modelName,
			Group:    "default",
			Status:   common.ChannelStatusEnabled,
			Priority: &priority,
			Weight:   &weight,
		}
		require.NoError(t, channel.Insert())
		idsByPriority[p] = append(idsByPriority[p], channel.Id)
	}
	InitChannelCache()

	for _, tc := range []struct {
		retry int
		want  []int
	}{
		{0, idsByPriority[5]},
		{1, idsByPriority[3]},
		{2, idsByPriority[1]},
		{3, idsByPriority[1]}, // retry 超出层数时复用最低层
	} {
		got := make(map[int]bool)
		for range 200 {
			channel, err := GetRandomSatisfiedChannel("default", modelName, tc.retry, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			got[channel.Id] = true
		}
		want := slices.Sorted(slices.Values(tc.want))
		assert.Equal(t, want, slices.Sorted(maps.Keys(got)), "retry %d", tc.retry)
	}
}

// [FORK] 查询表：行存在即采用（nil 视作 0，对齐 GetChannel）；行不存在返回 ok=false
func TestPriorityLookup(t *testing.T) {
	zero, five := int64(0), int64(5)
	abilities := []*Ability{
		{Group: "default", Model: "a", ChannelId: 1, Priority: &five},
		{Group: "default", Model: "a", ChannelId: 2, Priority: &zero},
		{Group: "default", Model: "a", ChannelId: 3, Priority: nil}, // nil → 0
		{Group: "other", Model: "a", ChannelId: 1, Priority: &zero},
	}
	lookup := buildPriorityLookup(abilities)
	for _, tc := range []struct {
		group, model string
		channelID    int
		want         int64
		ok           bool
	}{
		{"default", "a", 1, 5, true},
		{"default", "a", 2, 0, true},
		{"default", "a", 3, 0, true},  // nil 视作 0
		{"other", "a", 1, 0, true},    // group 隔离
		{"default", "b", 1, 0, false}, // model 不存在
		{"default", "a", 9, 0, false}, // channel 不存在
	} {
		got, ok := lookup.priorityOf(tc.group, tc.model, tc.channelID)
		assert.Equal(t, tc.ok, ok, "%s/%s/%d ok", tc.group, tc.model, tc.channelID)
		assert.Equal(t, tc.want, got, "%s/%s/%d value", tc.group, tc.model, tc.channelID)
	}
	// 零值查询表不 panic，一律查不到
	var empty priorityLookup
	_, ok := empty.priorityOf("default", "a", 1)
	assert.False(t, ok)

	// effectivePriority：有行用行值，没行回退 channels.priority，渠道不存在返回 0
	previousLookup, previousIDM := priorityLookupCache, channelsIDM
	t.Cleanup(func() { priorityLookupCache, channelsIDM = previousLookup, previousIDM })
	channelPriority, fallbackPriority := int64(7), int64(1)
	priorityLookupCache = lookup
	channelsIDM = map[int]*Channel{2: {Id: 2, Priority: &channelPriority}, 9: {Id: 9, Priority: &fallbackPriority}}
	assert.Equal(t, int64(5), effectivePriority("default", "a", 1))
	assert.Equal(t, int64(0), effectivePriority("default", "a", 2), "有行时用行值，不回退渠道")
	assert.Equal(t, int64(1), effectivePriority("default", "b", 9), "没有行时回退 channels.priority")
	assert.Equal(t, int64(0), effectivePriority("default", "b", 8), "渠道也不存在")
}

// [FORK] 查询表必须覆盖候选列表的每个 key：InitChannelCache 之后，凡出现在
// group2model2channels 里的 (group, model, channel)，要么查得到 priority，
// 要么该渠道确实没有对应的 abilities 行（此时由 effectivePriority 回退到 channels.priority）。
func TestPriorityLookupMatchesCandidates(t *testing.T) {
	useScheduledChannelDatabase(t)
	for i, spec := range []struct {
		models   string
		priority int64
	}{
		{"lookup-model-a", 5},
		{"lookup-model-a,lookup-model-b", 3},
		{"lookup-model-c", 1},
	} {
		weight := uint(1)
		channel := &Channel{
			Name:     fmt.Sprintf("lookup-c%d", i),
			Key:      "test-only",
			Models:   spec.models,
			Group:    "default",
			Status:   common.ChannelStatusEnabled,
			Priority: &spec.priority,
			Weight:   &weight,
		}
		require.NoError(t, channel.Insert())
	}
	InitChannelCache()

	type candidate struct {
		group, model string
		channelID    int
	}
	var candidates []candidate
	channelSyncLock.RLock()
	lookup := priorityLookupCache
	for group, model2channels := range group2model2channels {
		for modelName, ids := range model2channels {
			for _, id := range ids {
				candidates = append(candidates, candidate{group, modelName, id})
			}
		}
	}
	channelSyncLock.RUnlock()
	require.NotEmpty(t, candidates)

	for _, c := range candidates {
		if _, ok := lookup.priorityOf(c.group, c.model, c.channelID); ok {
			continue
		}
		var count int64
		require.NoError(t, DB.Model(&Ability{}).
			Where(commonGroupCol+" = ? and model = ? and channel_id = ?", c.group, c.model, c.channelID).
			Count(&count).Error)
		assert.Zero(t, count, "%s/%s/#%d 不在查询表里，却存在 abilities 行", c.group, c.model, c.channelID)
	}
}

// [FORK] 按供应关系设优先级：同渠道服务两个模型，只抬其中一个模型这一行的
// abilities.priority，只有该模型优先走这个渠道，另一个模型的分层不变。
func TestRoutingPriorityPerModel(t *testing.T) {
	useScheduledChannelDatabase(t)
	const (
		modelA = "per-model-a"
		modelB = "per-model-b"
	)
	weight, priority := uint(1), int64(1)
	var channels []*Channel
	for i := range 3 {
		channel := &Channel{
			Name:     fmt.Sprintf("per-model-c%d", i),
			Key:      "test-only",
			Models:   modelA + "," + modelB,
			Group:    "default",
			Status:   common.ChannelStatusEnabled,
			Priority: &priority,
			Weight:   &weight,
		}
		require.NoError(t, channel.Insert())
		channels = append(channels, channel)
	}
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelA, channels[0].Id).
		Update("priority", int64(5))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)
	InitChannelCache()

	collect := func(modelName string) map[int]bool {
		got := make(map[int]bool)
		for range 200 {
			channel, err := GetRandomSatisfiedChannel("default", modelName, 0, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			got[channel.Id] = true
		}
		return got
	}

	assert.Equal(t, map[int]bool{channels[0].Id: true}, collect(modelA), "模型 A 只走被抬高优先级的渠道")

	all := make(map[int]bool)
	for _, channel := range channels {
		all[channel.Id] = true
	}
	assert.Equal(t, all, collect(modelB), "模型 B 的分层不受 A 的单条优先级影响")
}

// [FORK] 缺 abilities 行时逐渠道回退 channels.priority：该渠道仍参与选路，
// 不因缺行被排除，也不被当成 0 优先级。
func TestPriorityFallbackPerChannel(t *testing.T) {
	useScheduledChannelDatabase(t)
	const modelName = "fallback-model"
	weight, channelPriority := uint(1), int64(3)
	var channels []*Channel
	for i := range 2 {
		channel := &Channel{
			Name:     fmt.Sprintf("fallback-c%d", i),
			Key:      "test-only",
			Models:   modelName,
			Group:    "default",
			Status:   common.ChannelStatusEnabled,
			Priority: &channelPriority,
			Weight:   &weight,
		}
		require.NoError(t, channel.Insert())
		channels = append(channels, channel)
	}
	// 先把第二个渠道的单条优先级抬到 6，确认查询表在起作用
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channels[1].Id).
		Update("priority", int64(6))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)
	InitChannelCache()

	pick := func(retry int) map[int]bool {
		got := make(map[int]bool)
		for range 200 {
			channel, err := GetRandomSatisfiedChannel("default", modelName, retry, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			got[channel.Id] = true
		}
		return got
	}
	assert.Equal(t, map[int]bool{channels[1].Id: true}, pick(0))
	assert.Equal(t, map[int]bool{channels[0].Id: true}, pick(1))

	// 删掉第二个渠道的 abilities 行之后，它仍参与选路，优先级回退 channels.priority=3
	require.NoError(t, DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channels[1].Id).
		Delete(&Ability{}).Error)
	InitChannelCache()

	both := map[int]bool{channels[0].Id: true, channels[1].Id: true}
	assert.Equal(t, both, pick(0), "缺行的渠道回退 channels.priority，与另一渠道同层")
	assert.Equal(t, both, pick(1))
}

// [FORK] 缓存开/关两条选路路径对同一份数据必须给出相同结果（能抓住 nil 语义写错）。
// 每层只有一个渠道，weight 平滑方式的差异不影响结果。
func TestPriorityCacheConsistency(t *testing.T) {
	useScheduledChannelDatabase(t)
	const modelName = "consistency-model"
	byPriority := make(map[int64]int)
	for i, priority := range []int64{5, 3, 1} {
		p, weight := priority, uint(1)
		channel := &Channel{
			Name:     fmt.Sprintf("consistency-c%d", i),
			Key:      "test-only",
			Models:   modelName,
			Group:    "default",
			Status:   common.ChannelStatusEnabled,
			Priority: &p,
			Weight:   &weight,
		}
		require.NoError(t, channel.Insert())
		byPriority[p] = channel.Id
	}

	route := func() map[int]int {
		InitChannelCache()
		got := make(map[int]int)
		for retry := range 3 {
			channel, err := GetRandomSatisfiedChannel("default", modelName, retry, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			got[retry] = channel.Id
		}
		return got
	}

	previousMemoryCache := common.MemoryCacheEnabled
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	common.MemoryCacheEnabled = true
	cached := route()
	common.MemoryCacheEnabled = false
	direct := route()

	assert.Equal(t, map[int]int{0: byPriority[5], 1: byPriority[3], 2: byPriority[1]}, cached)
	assert.Equal(t, cached, direct, "缓存开/关的选路结果必须一致")
}

// [FORK] UpdateAbilities 重建时保留单条优先级：编辑渠道（名称/权重/模型列表）
// 不再把模型上架页设的 priority 清回 channels.priority。只保留 priority，
// enabled/weight/tag 仍按当前渠道状态重建；模型列表里删掉的模型不残留。
func TestUpdateAbilitiesKeepsPerModelPriority(t *testing.T) {
	useScheduledChannelDatabase(t)
	channelPriority, weight := int64(3), uint(1)
	channel := &Channel{
		Name:     "keep-priority",
		Key:      "test-only",
		Models:   "keep-a,keep-b,keep-c",
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: &channelPriority,
		Weight:   &weight,
	}
	require.NoError(t, channel.Insert())

	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", "keep-a", channel.Id).
		Update("priority", int64(5))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)
	// 一条没有单独设过优先级的行（NULL），重建后必须仍是 NULL 而不是被填成 channel.Priority。
	cleared := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", "keep-c", channel.Id).
		Update("priority", nil)
	require.NoError(t, cleared.Error)
	require.EqualValues(t, 1, cleared.RowsAffected)

	priorityOf := func(modelName string) (*int64, bool) {
		var ability Ability
		err := DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channel.Id).
			First(&ability).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false
		}
		require.NoError(t, err)
		return ability.Priority, true
	}

	require.NoError(t, channel.UpdateAbilities(nil))

	kept, ok := priorityOf("keep-a")
	require.True(t, ok)
	require.NotNil(t, kept)
	assert.Equal(t, int64(5), *kept, "单条优先级不被清回 channels.priority")

	rebuilt, ok := priorityOf("keep-b")
	require.True(t, ok)
	require.NotNil(t, rebuilt)
	assert.Equal(t, channelPriority, *rebuilt, "没单独设过的行按渠道优先级重建")

	stillNull, ok := priorityOf("keep-c")
	require.True(t, ok)
	// Ability.Priority 的 gorm tag 带 default:0，GORM 插入 nil 指针时跳过该列、落 0，
	// 所以这里读回 0 而不是 NULL；与「nil 视作 0」的选路口径等价，
	// 关键是不被重置成 channel.Priority。
	require.NotNil(t, stillNull)
	assert.Equal(t, int64(0), *stillNull, "没设过优先级的行不被填成 channels.priority")

	// 从模型列表里删掉 keep-b 后重建，该行不残留。
	channel.Models = "keep-a,keep-c"
	require.NoError(t, channel.UpdateAbilities(nil))
	_, ok = priorityOf("keep-b")
	assert.False(t, ok, "模型列表里删掉的模型不留 abilities 行")
	kept, ok = priorityOf("keep-a")
	require.True(t, ok)
	require.NotNil(t, kept)
	assert.Equal(t, int64(5), *kept)
}

// [FORK] 改渠道级 priority 必须真的改道（`PUT /api/channel/` 带 priority，内置控制台的
// 优先级 spinner / 编辑抽屉走的就是这条路径），而模型上架页设的单条覆盖不能被一起清掉。
// 「沿袭行」按编辑前的 channels.priority 认：等于它的行跟着新值走，其余保留。
func TestChannelUpdateAppliesChannelPriorityToInheritedRows(t *testing.T) {
	useScheduledChannelDatabase(t)
	const inheritModel, overrideModel = "update-inherit", "update-override"
	weight := uint(1)
	editable := &Channel{
		Name:     "update-a",
		Key:      "test-only",
		Models:   inheritModel + "," + overrideModel,
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(3)),
		Weight:   &weight,
	}
	require.NoError(t, editable.Insert())
	neighbour := &Channel{
		Name:     "update-b",
		Key:      "test-only",
		Models:   inheritModel,
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(5)),
		Weight:   &weight,
	}
	require.NoError(t, neighbour.Insert())
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", overrideModel, editable.Id).
		Update("priority", int64(7))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)

	priorityOf := func(modelName string, channelId int) *int64 {
		var ability Ability
		require.NoError(t, DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channelId).
			First(&ability).Error)
		return ability.Priority
	}
	pick := func(retry int) map[int]bool {
		got := make(map[int]bool)
		for range 200 {
			channel, err := GetRandomSatisfiedChannel("default", inheritModel, retry, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			got[channel.Id] = true
		}
		return got
	}

	InitChannelCache()
	assert.Equal(t, map[int]bool{neighbour.Id: true}, pick(0))
	assert.Equal(t, map[int]bool{editable.Id: true}, pick(1))

	edited, err := GetChannelById(editable.Id, true)
	require.NoError(t, err)
	edited.Priority = lo.ToPtr(int64(10))
	require.NoError(t, edited.Update())
	assert.Equal(t, int64(10), edited.GetPriority(), "channels.priority 按请求写入")

	inherited := priorityOf(inheritModel, editable.Id)
	require.NotNil(t, inherited)
	assert.Equal(t, int64(10), *inherited, "沿袭渠道级的行跟着新值走")
	overridden := priorityOf(overrideModel, editable.Id)
	require.NotNil(t, overridden)
	assert.Equal(t, int64(7), *overridden, "单条覆盖不被改渠道级 priority 清掉")

	InitChannelCache()
	assert.Equal(t, map[int]bool{editable.Id: true}, pick(0), "改渠道级 priority 后选路随之变化")
	assert.Equal(t, map[int]bool{neighbour.Id: true}, pick(1))
}

// [FORK] tag 编辑同时带 models/group 时会重建 abilities，priority 也要按编辑前的渠道级值
// 判断沿袭与覆盖（`EditChannelByTag` 先按 id 读旧值，因为一条 tag 下有多个渠道）。
func TestEditChannelByTagAppliesChannelPriorityToInheritedRows(t *testing.T) {
	useScheduledChannelDatabase(t)
	weight := uint(1)
	channel := &Channel{
		Name:     "tag-edit",
		Key:      "test-only",
		Models:   "tag-inherit,tag-override",
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(3)),
		Weight:   &weight,
		Tag:      lo.ToPtr("fork-tag-edit"),
	}
	require.NoError(t, channel.Insert())
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", "tag-override", channel.Id).
		Update("priority", int64(7))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)

	models := "tag-inherit,tag-override,tag-new"
	require.NoError(t, EditChannelByTag("fork-tag-edit", nil, nil, &models, nil, lo.ToPtr(int64(10)), nil, nil, nil))

	priorityOf := func(modelName string) *int64 {
		var ability Ability
		require.NoError(t, DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channel.Id).
			First(&ability).Error)
		return ability.Priority
	}
	inherited := priorityOf("tag-inherit")
	require.NotNil(t, inherited)
	assert.Equal(t, int64(10), *inherited, "沿袭渠道级的行跟着新值走")
	overridden := priorityOf("tag-override")
	require.NotNil(t, overridden)
	assert.Equal(t, int64(7), *overridden, "单条覆盖保留")
	added := priorityOf("tag-new")
	require.NotNil(t, added)
	assert.Equal(t, int64(10), *added, "新行用新的渠道级 priority")
}

// [FORK] 改 channels.group 时保留键 group|model 会失配，此时退化为按 model 迁移，且只从
// 「新旧 group 集合的交集」取值：单条覆盖不因为换组而丢失（曾经是静默数据丢失）。
func TestChannelGroupChangeKeepsPerModelPriority(t *testing.T) {
	useScheduledChannelDatabase(t)
	const modelName = "group-change-model"
	channel := &Channel{
		Name:     "group-change",
		Key:      "test-only",
		Models:   modelName + ",group-plain-model",
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(3)),
		Weight:   lo.ToPtr(uint(1)),
	}
	require.NoError(t, channel.Insert())
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channel.Id).
		Update("priority", int64(5))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)

	edited, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	edited.Group = "vip"
	require.NoError(t, edited.Update())

	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Order(commonGroupCol+", model").Find(&abilities).Error)
	require.Len(t, abilities, 2, "旧 group 的行随重建消失，新 group 各一行")
	kept := map[string]*int64{}
	for _, ability := range abilities {
		require.Equal(t, "vip", ability.Group)
		kept[ability.Model] = ability.Priority
	}
	require.NotNil(t, kept[modelName])
	assert.Equal(t, int64(5), *kept[modelName], "单条覆盖按 model 迁移到新 group")
	require.NotNil(t, kept["group-plain-model"])
	assert.Equal(t, int64(3), *kept["group-plain-model"], "没设过覆盖的行仍用渠道级值")
}

// [FORK] tag 批量编辑只带 priority（不带 models/group）也要保留单条 priority：这条路径
// 曾经走 `UPDATE abilities SET priority` 把该 tag 下所有行的覆盖一起改掉，且不走
// UpdateAbilities 的渠道行锁。
func TestEditChannelByTagPriorityOnlyKeepsOverrides(t *testing.T) {
	useScheduledChannelDatabase(t)
	const shared, overridden = "tagonly-shared", "tagonly-overridden"
	tag := lo.ToPtr("fork-tag-priority")
	weight := uint(1)
	owner := &Channel{
		Name:     "tag-priority-a",
		Key:      "test-only",
		Models:   shared + "," + overridden,
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(3)),
		Weight:   &weight,
		Tag:      tag,
	}
	require.NoError(t, owner.Insert())
	sibling := &Channel{
		Name:     "tag-priority-b",
		Key:      "test-only",
		Models:   shared,
		Group:    "default",
		Status:   common.ChannelStatusEnabled,
		Priority: lo.ToPtr(int64(3)),
		Weight:   &weight,
		Tag:      tag,
	}
	require.NoError(t, sibling.Insert())
	raised := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", overridden, owner.Id).
		Update("priority", int64(7))
	require.NoError(t, raised.Error)
	require.EqualValues(t, 1, raised.RowsAffected)

	require.NoError(t, EditChannelByTag("fork-tag-priority", nil, nil, nil, nil, lo.ToPtr(int64(10)), nil, nil, nil))

	priorityOf := func(modelName string, channelId int) *int64 {
		var ability Ability
		require.NoError(t, DB.Where(commonGroupCol+" = ? and model = ? and channel_id = ?", "default", modelName, channelId).
			First(&ability).Error)
		return ability.Priority
	}
	inherited := priorityOf(shared, owner.Id)
	require.NotNil(t, inherited)
	assert.Equal(t, int64(10), *inherited, "沿袭渠道级的行跟着新值走")
	siblingInherited := priorityOf(shared, sibling.Id)
	require.NotNil(t, siblingInherited)
	assert.Equal(t, int64(10), *siblingInherited, "同 tag 的其它渠道同样生效")
	overrideKept := priorityOf(overridden, owner.Id)
	require.NotNil(t, overrideKept)
	assert.Equal(t, int64(7), *overrideKept, "单条覆盖不被 tag 批量改 priority 清掉")
}
