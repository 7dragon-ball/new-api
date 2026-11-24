package model

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	var abilities []AbilityWithChannel
	err := DB.Table("abilities").
		Select("abilities.*, channels.type as channel_type").
		Joins("left join channels on abilities.channel_id = channels.id").
		Where("abilities.enabled = ?", true).
		Scan(&abilities).Error
	return abilities, err
}

func GetGroupEnabledModels(group string) []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).Distinct("model").Pluck("model", &models)
	return models
}

func GetEnabledModels() []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models)
	return models
}

func GetAllEnableAbilities() []Ability {
	var abilities []Ability
	DB.Find(&abilities, "enabled = ?", true)
	return abilities
}

func getPriority(group string, model string, retry int) (int, error) {

	var priorities []int
	err := DB.Model(&Ability{}).
		Select("DISTINCT(priority)").
		Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true).
		Order("priority DESC").              // 按优先级降序排序
		Pluck("priority", &priorities).Error // Pluck用于将查询的结果直接扫描到一个切片中

	if err != nil {
		// 处理错误
		return 0, err
	}

	if len(priorities) == 0 {
		// 如果没有查询到优先级，则返回错误
		return 0, errors.New("database consistency is broken")
	}

	// 确定要使用的优先级
	var priorityToUse int
	if retry >= len(priorities) {
		// 如果重试次数大于优先级数，则使用最小的优先级
		priorityToUse = priorities[len(priorities)-1]
	} else {
		priorityToUse = priorities[retry]
	}
	return priorityToUse, nil
}

func getChannelQuery(group string, model string, retry int) (*gorm.DB, error) {
	maxPrioritySubQuery := DB.Model(&Ability{}).Select("MAX(priority)").Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true)
	channelQuery := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ? and priority = (?)", group, model, true, maxPrioritySubQuery)
	if retry != 0 {
		priority, err := getPriority(group, model, retry)
		if err != nil {
			return nil, err
		} else {
			channelQuery = DB.Where(commonGroupCol+" = ? and model = ? and enabled = ? and priority = ?", group, model, true, priority)
		}
	}

	return channelQuery, nil
}

func GetChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	var abilities []Ability
	err := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true).Order("priority DESC, weight DESC").Find(&abilities).Error
	if err != nil {
		return nil, err
	}
	abilities = filterAbilitiesByConstraints(abilities, model, filters)
	// Like the memory cache, retry with the routing name (for example without
	// @ modifiers) when no channel serves the exact name.
	if normalized := ratio_setting.RoutingMatchModelName(model); len(abilities) == 0 && normalized != "" && normalized != model {
		err = DB.Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, normalized, true).Order("priority DESC, weight DESC").Find(&abilities).Error
		if err != nil {
			return nil, err
		}
		abilities = filterAbilitiesByConstraints(abilities, model, filters)
	}
	if len(abilities) > 0 {
		priorities := make([]int64, 0)
		seen := make(map[int64]bool)
		for _, ability := range abilities {
			priority := int64(0)
			if ability.Priority != nil {
				priority = *ability.Priority
			}
			if !seen[priority] {
				seen[priority] = true
				priorities = append(priorities, priority)
			}
		}
		sort.Slice(priorities, func(i, j int) bool { return priorities[i] > priorities[j] })
		if retry >= len(priorities) {
			retry = len(priorities) - 1
		}
		targetPriority := priorities[retry]
		abilities = lo.Filter(abilities, func(ability Ability, _ int) bool {
			return ability.Priority == nil && targetPriority == 0 || ability.Priority != nil && *ability.Priority == targetPriority
		})
	}
	channel := Channel{}
	if len(abilities) > 0 {
		// Randomly choose one
		weightSum := uint(0)
		for _, ability_ := range abilities {
			weightSum += ability_.Weight + 10
		}
		// Randomly choose one
		weight := common.GetRandomInt(int(weightSum))
		for _, ability_ := range abilities {
			weight -= int(ability_.Weight) + 10
			//log.Printf("weight: %d, ability weight: %d", weight, *ability_.Weight)
			if weight <= 0 {
				channel.Id = ability_.ChannelId
				break
			}
		}
	} else {
		return nil, nil
	}
	err = DB.First(&channel, "id = ?", channel.Id).Error
	return &channel, err
}

// filterAbilitiesByConstraints applies the same ChannelSatisfiesFilters
// predicate used by the memory-cache path. A failed channel lookup fails
// closed when a task-plugin identity is required and fails open otherwise.
func filterAbilitiesByConstraints(abilities []Ability, modelName string, filters []dto.ChannelFilter) []Ability {
	if len(abilities) == 0 {
		return nil
	}

	channelIds := make([]int, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, ok := seen[ability.ChannelId]; ok {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIds = append(channelIds, ability.ChannelId)
	}

	var channels []*Channel
	if err := DB.Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
		if identityFilterRequiresKey(filters) {
			return nil
		}
		return abilities
	}

	channelsByID := make(map[int]*Channel, len(channels))
	for _, channel := range channels {
		channelsByID[channel.Id] = channel
	}

	filtered := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		channel := channelsByID[ability.ChannelId]
		if ok, _ := ChannelSatisfiesFilters(channel, modelName, filters); ok {
			filtered = append(filtered, ability)
		}
	}
	return filtered
}

func identityFilterRequiresKey(filters []dto.ChannelFilter) bool {
	for _, filter := range filters {
		if filter.Kind == dto.FilterTaskPluginIdentity && filter.TaskPluginKey != "" {
			return true
		}
	}
	return false
}

func (channel *Channel) AddAbilities(tx *gorm.DB) error {
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	// choose DB or provided tx
	useDB := DB
	if tx != nil {
		useDB = tx
	}
	for _, chunk := range lo.Chunk(abilities, 50) {
		err := useDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) DeleteAbilities() error {
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

// priorityValue 把可空的 priority 归一成选路时使用的数值（nil 视作 0）。
// 只用于「abilities 行是否沿袭渠道级 priority」的比较，不能替代 equalPriority：
// channel_priority.go 的 undo 需要区分 NULL 与 0。
func priorityValue(priority *int64) int64 {
	if priority == nil {
		return 0
	}
	return *priority
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities(tx *gorm.DB) error {
	isNewTx := false
	// 如果没有传入事务，创建新的事务
	if tx == nil {
		tx = DB.Begin()
		if tx.Error != nil {
			return tx.Error
		}
		isNewTx = true
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
			}
		}()
	}

	// [FORK] 重建是「读旧 priority → 删 → 插」的读改写，先锁住渠道行把它与单条优先级
	// 写入串行化：UpdateAbilityPriority 走同一把锁，否则两者交错时重建会用读到的旧值
	// 覆盖掉这次写入，或在行被删掉的那一瞬间让写入影响 0 行、误报 404。锁序统一为
	// 「channel 行 → abilities 行」，与 AddAbility / DeleteAbility 一致。渠道正在被删除时
	// 锁不到行，此时照常重建（渠道删除会连带删掉 abilities，不是本函数的职责）。
	if err := lockForUpdate(tx).Select("id").Where("id = ?", channel.Id).Find(&Channel{}).Error; err != nil {
		if isNewTx {
			tx.Rollback()
		}
		return err
	}

	// [FORK] 重建时保留该渠道已有 abilities 的 priority。
	//
	// 为什么保留：abilities.priority 是本 fork 的选路依据，而它可能来自模型上架页的单条
	// 设置（PUT /api/channel/ability/priority，只写 abilities、不写 channels）。若重建时
	// 一律按 channels.priority 重算，管理员设过的单条优先级会被「改渠道名 / 权重 / 模型
	// 列表 / 批量打标签 / 上游模型同步」任一日常动作清空。
	//
	// 为什么不是无条件保留：改渠道级 priority（PUT /api/channel/ 带 priority，内置控制台
	// 的优先级 spinner / 编辑抽屉；且同样随二进制发布、可达）必须真的改道，否则就是返回
	// 200 而选路不变的静默契约变更。所以调用方把编辑前的 channels.priority
	// 填进 channel.PreviousPriority：与之相等的行是「沿袭渠道级」，跟着新值走；与之不同的是
	// 单条覆盖，原样保留。单条覆盖恰好等于渠道级值的情况无法区分，按沿袭处理（下次改
	// 渠道级 priority 时会被带走）。没填（nil）表示调用方没动渠道级 priority，一切沿用。
	//
	// group 变化（default → vip）时 group|model 键失配，保留键退化为 model：同名的行沿用
	// 该 model 的 priority（多个旧 group 给出不同值时取较大者，既不压低已有优先级，结果也
	// 确定）。不用「新旧 group 有无交集」做条件：本产品只建单 group 渠道，一换组就必然无交集，
	// 那等于换一个说法保留静默丢失。代价是换了 priority 量级不同的 group 时旧值会被带过去，
	// 要回渠道级值用 PUT /api/channel/ability/priority 单条改，或跑 FixAbility 重建。
	var existingAbilities []Ability
	err := tx.Select(commonGroupCol+", model, priority").
		Where("channel_id = ?", channel.Id).Find(&existingAbilities).Error
	if err != nil {
		if isNewTx {
			tx.Rollback()
		}
		return err
	}
	keptByRelation := make(map[string]*int64, len(existingAbilities))
	keptByModel := make(map[string]*int64, len(existingAbilities))
	for _, existing := range existingAbilities {
		// nil 也原样传回：GORM 对带 default:0 的 nil 指针落 0，与选路「nil 视作 0」一致。
		keptByRelation[existing.Group+"|"+existing.Model] = existing.Priority
		// 同一 model 在多个旧 group 下给出不同值时取较大者：结果确定，且不压低已有优先级。
		value := int64(0)
		if existing.Priority != nil {
			value = *existing.Priority
		}
		kept, ok := keptByModel[existing.Model]
		keptValue := int64(0)
		if kept != nil {
			keptValue = *kept
		}
		if !ok || value > keptValue {
			keptByModel[existing.Model] = existing.Priority
		}
	}

	// First delete all abilities of this channel
	err = tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
	if err != nil {
		if isNewTx {
			tx.Rollback()
		}
		return err
	}

	// Then add new abilities
	models_ := channel.GetModels()
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	previousPriority := channel.PreviousPriority
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			// [FORK] 已有行沿用它的优先级（含 nil），新行用渠道优先级。见上面说明。
			priority := channel.Priority
			kept, ok := keptByRelation[key]
			if !ok {
				kept, ok = keptByModel[model]
			}
			if ok {
				priority = kept
				// 等于编辑前的渠道级值 = 沿袭行，跟随新值；其余是单条覆盖，保留。
				// 比较前两边都归一成数值：abilities.priority 可能是 SQL NULL（旧数据、手工写入），
				// channels.priority 也可能是 NULL，两者在选路上都按 0 参与。直接比指针会把
				// 「NULL 沿袭行」误判成单条覆盖，改渠道级 priority 时它不跟随。
				if previousPriority != nil && priorityValue(kept) == *previousPriority {
					priority = channel.Priority
				}
			}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}

	if len(abilities) > 0 {
		for _, chunk := range lo.Chunk(abilities, 50) {
			err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
			if err != nil {
				if isNewTx {
					tx.Rollback()
				}
				return err
			}
		}
	}

	// 如果是新创建的事务，需要提交
	if isNewTx {
		return tx.Commit().Error
	}

	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return DB.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityStatusByTag(tag string, status bool) error {
	return DB.Model(&Ability{}).Where("tag = ?", tag).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityByTag(tag string, newTag *string, priority *int64, weight *uint) error {
	ability := Ability{}
	if newTag != nil {
		ability.Tag = newTag
	}
	if priority != nil {
		ability.Priority = priority
	}
	if weight != nil {
		ability.Weight = *weight
	}
	return DB.Model(&Ability{}).Where("tag = ?", tag).Updates(ability).Error
}

var fixLock = sync.Mutex{}

func FixAbility() (int, int, error) {
	lock := fixLock.TryLock()
	if !lock {
		return 0, 0, common.NewMessage("A repair task is already running. Please try again later")
	}
	defer fixLock.Unlock()

	// truncate abilities table
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		err := DB.Exec("DELETE FROM abilities").Error
		if err != nil {
			common.SysLog(common.LogText("Delete abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	} else {
		err := DB.Exec("TRUNCATE TABLE abilities").Error
		if err != nil {
			common.SysLog(common.LogText("Truncate abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	}
	var channels []*Channel
	// Find all channels
	err := DB.Model(&Channel{}).Find(&channels).Error
	if err != nil {
		return 0, 0, err
	}
	if len(channels) == 0 {
		return 0, 0, nil
	}
	successCount := 0
	failCount := 0
	for _, chunk := range lo.Chunk(channels, 50) {
		ids := lo.Map(chunk, func(c *Channel, _ int) int { return c.Id })
		// Delete all abilities of this channel
		err = DB.Where("channel_id IN ?", ids).Delete(&Ability{}).Error
		if err != nil {
			common.SysLog(common.LogText("Delete abilities failed: %s", err.Error()))
			failCount += len(chunk)
			continue
		}
		// Then add new abilities
		for _, channel := range chunk {
			err = channel.AddAbilities(nil)
			if err != nil {
				common.SysLog(common.LogText("Add abilities for channel %d failed: %s", channel.Id, err.Error()))
				failCount++
			} else {
				successCount++
			}
		}
	}
	InitChannelCache()
	BumpSyncTopic(SyncTopicChannels)
	return successCount, failCount, nil
}
