-- +goose Up
-- 身份快照：写入时固化，用户/部门被删后仍能按部门聚合历史用量。
-- DEFAULT '' 必需：运行时聚合键用空串比较，NULL 匹配不上会拆行。
ALTER TABLE IF EXISTS quota_data ADD COLUMN IF NOT EXISTS display_name VARCHAR(64) DEFAULT '';
ALTER TABLE IF EXISTS quota_data ADD COLUMN IF NOT EXISTS department   VARCHAR(30) DEFAULT '';

-- +goose Down
-- 不回滚：删列会连快照一起丢。
