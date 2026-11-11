-- Preserve the existing symbology/value lookup while extending it for stable
-- independent-ID paging, and support exact value lookups without symbology.
DROP INDEX idx_barcode_lookup;
CREATE INDEX idx_barcode_lookup ON item_barcodes(symbology,normalized_value,id);
CREATE INDEX idx_barcode_value_id ON item_barcodes(normalized_value,id);
