-- +goose Up
-- 身份快照：兑换那一刻固化兑换人，用户被删后列表仍显示原名字。
ALTER TABLE IF EXISTS redemptions ADD COLUMN IF NOT EXISTS used_username     VARCHAR(64);
ALTER TABLE IF EXISTS redemptions ADD COLUMN IF NOT EXISTS used_display_name VARCHAR(64);

-- +goose Down
-- 不回滚：删列会连快照一起丢。
