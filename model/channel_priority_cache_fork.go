package model

// [FORK] 选路用优先级查询表：(group, model, channel_id) → priority。
// 候选集合仍由 group2model2channels 决定（channels 表派生），本表只覆盖
// 「这条供应关系在这个模型下的优先级」，缺项回退 channel.GetPriority()。
type priorityLookup map[string]map[string]map[int]int64

// priorityLookupCache 是 InitChannelCache 与候选列表一起发布的查询表。
// [FORK] 上游没有这张表：缓存选路只读 channels.priority，而按供应关系设优先级
// 要读 abilities.priority。读写都由 channelSyncLock 保护（写方持写锁，选路持读锁）。
var priorityLookupCache priorityLookup

// [FORK] 由 abilities 行构建查询表。行存在即采用其 priority（NULL 视作 0，
// 与 GetChannel 的口径一致，见 ability.go 的 DB 路径）；行不存在由调用方回退 channels.priority。
func buildPriorityLookup(abilities []*Ability) priorityLookup {
	lookup := make(priorityLookup, len(abilities))
	for _, ability := range abilities {
		byModel, ok := lookup[ability.Group]
		if !ok {
			byModel = make(map[string]map[int]int64)
			lookup[ability.Group] = byModel
		}
		byChannel, ok := byModel[ability.Model]
		if !ok {
			byChannel = make(map[int]int64)
			byModel[ability.Model] = byChannel
		}
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		byChannel[ability.ChannelId] = priority
	}
	return lookup
}

// [FORK] 查一条供应关系的优先级；查不到返回 (0, false)，由调用方回退渠道优先级。
// 零值（nil）查询表同样安全，一律返回 false。
func (l priorityLookup) priorityOf(group, model string, channelID int) (int64, bool) {
	priority, ok := l[group][model][channelID]
	return priority, ok
}

// [FORK] 一条供应关系的有效优先级：abilities 行存在用它的值（nil 视作 0），否则回退渠道优先级。
// 调用方必须已持有 channelSyncLock 读锁（它是 channelsIDM 的读锁）。
func effectivePriority(group, model string, channelID int) int64 {
	if priority, ok := priorityLookupCache.priorityOf(group, model, channelID); ok {
		return priority
	}
	if channel, ok := channelsIDM[channelID]; ok {
		return channel.GetPriority()
	}
	return 0
}
