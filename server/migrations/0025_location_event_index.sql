-- Read one owner's immutable movement records in stable independent-ID order.
CREATE INDEX idx_location_events_location_id ON location_events(location_id,id);
