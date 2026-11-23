-- +goose Up
-- 身份快照：写入时固化，用户/部门被删后仍能显示当时的名字。
ALTER TABLE IF EXISTS logs ADD COLUMN IF NOT EXISTS display_name VARCHAR(64);
ALTER TABLE IF EXISTS logs ADD COLUMN IF NOT EXISTS department   VARCHAR(30);
ALTER TABLE IF EXISTS logs ADD COLUMN IF NOT EXISTS channel_name TEXT;

-- +goose Down
-- 不回滚：删列会连快照一起丢。
