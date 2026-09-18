-- +goose NO TRANSACTION
-- +goose Up
-- Built CONCURRENTLY: the hook runs while the previous release's replicas
-- still insert outbox rows, and a plain CREATE INDEX would block them for
-- the whole build — the published-history index scans every retained row.
-- Non-transactional, so each statement is restartable on its own: a build
-- that failed leaves an INVALID index behind, and IF NOT EXISTS would keep
-- it, hence the drop before each create.
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_pending_idx;
-- Eligible rows in publication order.
CREATE INDEX CONCURRENTLY user_events_outbox_pending_idx
    ON user_events_outbox (created_at, id) WHERE published_at IS NULL AND quarantined_at IS NULL;
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_user_pending_idx;
-- Head-of-line check per user.
CREATE INDEX CONCURRENTLY user_events_outbox_user_pending_idx
    ON user_events_outbox (user_id, created_at, id) WHERE published_at IS NULL AND quarantined_at IS NULL;
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_published_idx;
-- Retention of published rows.
CREATE INDEX CONCURRENTLY user_events_outbox_published_idx
    ON user_events_outbox (published_at) WHERE published_at IS NOT NULL;
-- The old single-column index is superseded by the pending index.
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_unpublished_idx;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_published_idx;
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_user_pending_idx;
DROP INDEX CONCURRENTLY IF EXISTS user_events_outbox_pending_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS user_events_outbox_unpublished_idx
    ON user_events_outbox (created_at) WHERE published_at IS NULL;
