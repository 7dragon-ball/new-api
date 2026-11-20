package service

import "slices"

// [FORK] 批量重置密码的 step-up context：一份 proof 绑定本次要重置的完整账号集合。
// 集合参与 context hash，所以 proof 只能用于它被签发时确认的那一批账号。
type AdminUserPasswordBatchResetContext struct {
	UserIDs []int `json:"user_ids"`
}

// maxBatchPasswordResetUsers 是本接口自定的上限（现有两个批量接口分别是无上限与 1000）。
const maxBatchPasswordResetUsers = 100

// NormalizeBatchPasswordResetUserIDs validates an id set for the batch password
// reset and returns it in ascending order. Duplicates are rejected instead of
// being collapsed: a caller that named the same account twice asked for
// something the service cannot satisfy, and silently resetting fewer accounts
// than it asked for is worse than a clear error. A single order-independent
// set also means the proof context and the request body hash identically.
func NormalizeBatchPasswordResetUserIDs(ids []int) ([]int, bool) {
	if len(ids) == 0 || len(ids) > maxBatchPasswordResetUsers {
		return nil, false
	}
	normalized := slices.Clone(ids)
	slices.Sort(normalized)
	if normalized[0] <= 0 {
		return nil, false
	}
	if len(slices.Compact(slices.Clone(normalized))) != len(normalized) {
		return nil, false
	}
	return normalized, true
}
