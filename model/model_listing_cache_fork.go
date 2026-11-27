package model

import (
	"errors"
	"sync"
	"sync/atomic"
)

// [FORK] 「模型是否上架 / 是否已下架」判定的内存快照，供推理链路每次请求前校验。
//
// The snapshot keeps the *entire* candidate set, including disabled (status != 1)
// exact rows: whether a name is listed is a function of "does an exact row for
// this name exist, and what is its status", plus the wider prefix/suffix/contains
// rules. A retired exact entry shadows a broader enabled rule, so a plain
// "set of listed names" would not reproduce the catalog verdict. The catalog is
// admin-curated and small, and only three columns are read.
type modelListingCatalog struct {
	exact    map[string]int // model_name -> status (includes disabled rows)
	prefix   []Model        // ordered by id ascending
	suffix   []Model
	contains []Model
}

// matched mirrors IsModelListed precedence and returns the status of the first
// rule covering name, plus whether any rule matched at all. Exact wins outright
// (even when disabled), then prefix, then suffix, then contains; within a rule
// type the first row (lowest id) that matches wins.
func (cat *modelListingCatalog) matched(name string) (int, bool) {
	if cat == nil || name == "" {
		return 0, false
	}
	if status, ok := cat.exact[name]; ok {
		return status, true
	}
	for _, bucket := range [3][]Model{cat.prefix, cat.suffix, cat.contains} {
		if status, ok := firstListingRuleStatus(bucket, name); ok {
			return status, true
		}
	}
	return 0, false
}

// listed mirrors IsModelListed: a name is offered iff a catalog rule covers it
// and that rule is enabled.
func (cat *modelListingCatalog) listed(name string) bool {
	status, ok := cat.matched(name)
	return ok && status == 1
}

// retired reports a name that resolves to a catalog rule which is explicitly
// disabled. A name with no catalog entry is neither listed nor retired: it was
// never curated, so callers must not treat it as 下架.
func (cat *modelListingCatalog) retired(name string) bool {
	status, ok := cat.matched(name)
	return ok && status != 1
}

// firstListingRuleStatus returns the status of the first row matching name.
// It reuses Model.MatchesName so the matching logic has a single source.
func firstListingRuleStatus(rows []Model, name string) (int, bool) {
	for i := range rows {
		if rows[i].MatchesName(name) {
			return rows[i].Status, true
		}
	}
	return 0, false
}

var (
	modelListingCatalogPtr atomic.Pointer[modelListingCatalog]
	modelListingReloadMu   sync.Mutex
)

// InitModelListingCache rebuilds and publishes the listing snapshot. On failure
// nothing is published, so the previous snapshot (or the DB fallback) stays in
// effect instead of turning every request into "not listed".
func InitModelListingCache() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	modelListingReloadMu.Lock()
	defer modelListingReloadMu.Unlock()

	var records []Model
	// No status filter and no LIMIT: precedence is resolved in Go, and disabled
	// exact rows must be kept so they can shadow broader rules. Order("id") makes
	// the same-rule tie-break reproducible, matching IsModelListed.
	if err := DB.Model(&Model{}).
		Select("model_name", "name_rule", "status").
		Order("id").
		Find(&records).Error; err != nil {
		return err
	}

	catalog := &modelListingCatalog{exact: make(map[string]int)}
	for i := range records {
		record := &records[i]
		switch record.NameRule {
		case NameRuleExact:
			catalog.exact[record.ModelName] = record.Status
		case NameRulePrefix:
			catalog.prefix = append(catalog.prefix, *record)
		case NameRuleSuffix:
			catalog.suffix = append(catalog.suffix, *record)
		case NameRuleContains:
			catalog.contains = append(catalog.contains, *record)
		default:
			// Unknown rule values never match, in either implementation.
		}
	}
	// Copy-on-write: build off-lock, then publish the finished snapshot atomically.
	modelListingCatalogPtr.Store(catalog)
	return nil
}

// IsModelListedCached reports whether modelName is listed, using an in-memory
// snapshot with the exact same semantics as IsModelListed. When no snapshot has
// been published yet (startup race or a failed warm-up), it falls back to the
// database so a missing snapshot never rejects every request.
func IsModelListedCached(modelName string) (bool, error) {
	catalog := modelListingCatalogPtr.Load()
	if catalog == nil {
		return IsModelListed(modelName)
	}
	return catalog.listed(modelName), nil
}

// IsModelRetired reports whether modelName resolves to a catalog entry that is
// explicitly disabled (status != 1). Unlike IsModelListed, a name with no
// catalog entry is NOT retired: it was never curated, so callers keep their
// previous behavior. Same query and precedence as IsModelListed.
func IsModelRetired(modelName string) (bool, error) {
	if modelName == "" || DB == nil {
		return false, nil
	}
	var records []Model
	if err := DB.Model(&Model{}).
		Select("model_name", "name_rule", "status").
		Where("model_name = ? OR name_rule <> ?", modelName, NameRuleExact).
		Order("id").
		Find(&records).Error; err != nil {
		return false, err
	}
	policy := resolveModelMetadata(records, []string{modelName})[modelName]
	return policy != nil && policy.Status != 1, nil
}

// IsModelRetiredCached is the snapshot-backed form of IsModelRetired, with the
// same DB fallback when no snapshot has been published.
func IsModelRetiredCached(modelName string) (bool, error) {
	catalog := modelListingCatalogPtr.Load()
	if catalog == nil {
		return IsModelRetired(modelName)
	}
	return catalog.retired(modelName), nil
}
