-- Financial profile opt-in/replacement preserves exact reviewed before/after/input.
-- Existing profiles receive no invented history, timestamp or audit association.
CREATE TABLE asset_profile_changes (
 mutation_event_id TEXT PRIMARY KEY REFERENCES mutation_events(id) DEFERRABLE INITIALLY DEFERRED,
 item_unit_id TEXT NOT NULL REFERENCES asset_profiles(item_unit_id) ON DELETE RESTRICT,
 context_json TEXT NOT NULL CHECK(json_valid(context_json) AND json_type(context_json)='object')
) STRICT;
CREATE INDEX idx_asset_profile_changes_owner ON asset_profile_changes(item_unit_id,mutation_event_id);
CREATE TRIGGER asset_profile_changes_no_update BEFORE UPDATE ON asset_profile_changes
 BEGIN SELECT RAISE(ABORT,'asset profile history is immutable'); END;
CREATE TRIGGER asset_profile_changes_no_delete BEFORE DELETE ON asset_profile_changes
 BEGIN SELECT RAISE(ABORT,'asset profile history is immutable'); END;
