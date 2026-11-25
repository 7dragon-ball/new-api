package model

import (
	"sort"
	"strings"
)

// [FORK] 模型标签字典：去重、按字典序的标签全集。
//
// 为什么需要它：列表页改成服务端分页后，「标签」筛选框的候选只能从当前页 20 条
// 模型的 tags 里现算。某个关键词命中超过 20 条模型时，只在第 21 条之后出现的标签
// 就看不到建议了（手打仍能筛到，只是没提示）。这里直接给出全集。
//
// 与 SearchModels 的 tag 筛选同一口径：子串匹配，并且两边都用 LOWER 比较，
// 所以 PG 上不区分大小写。去重仍按小写，同一个小写形式只保留一种拼写（按 id 序
// 首次出现的那种）；因为筛选也不区分大小写，点选哪种拼写都能筛到全部变体。
//
// tags 在上游是逗号分隔的字符串列（models.tags varchar(255)），没有独立的标签表，
// 所以只能把列拉回来在内存里拆。
func ListModelTags(keyword string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	needle := strings.ToLower(strings.TrimSpace(keyword))
	var raw []string
	// 空 tags 的行不产出标签；NULL 也要排掉，否则 Pluck 进 []string 会报错
	db := DB.Model(&Model{}).Where("tags IS NOT NULL AND tags <> ''")
	if needle != "" {
		// 粗筛：先让数据库按子串收窄，下面再按「单个标签」精确收窄。
		// 与 SearchModels 一样走 LOWER，PG 上不会因为大小写漏掉整格。
		db = db.Where("LOWER(tags) LIKE ?", "%"+needle+"%")
	}
	// 去重保留「首次出现」的拼写，按 id 排序让首次出现可复现（Pluck 本身不保证顺序）
	if err := db.Order("id").Pluck("tags", &raw).Error; err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(raw))
	tags := make([]string, 0, len(raw))
	for _, row := range raw {
		for _, tag := range strings.Split(row, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			// 数据库的 LIKE 是整串匹配（"vision,旗舰" 命中 keyword="vision"），
			// 但用户要的是「单个标签含这个词」，这里再收一次
			if needle != "" && !strings.Contains(strings.ToLower(tag), needle) {
				continue
			}
			key := strings.ToLower(tag)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	if len(tags) > limit {
		tags = tags[:limit]
	}
	return tags, nil
}
