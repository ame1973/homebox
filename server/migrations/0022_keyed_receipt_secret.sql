-- Pin a server secret only after an actually keyed domain receipt commits.
-- Existing receipts, including post_media, may use an older unkeyed protocol.
CREATE TABLE auth_keyed_receipt_secret (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 key_proof BLOB NOT NULL CHECK(length(key_proof)=32)
) STRICT;

CREATE TRIGGER auth_keyed_receipt_secret_no_update BEFORE UPDATE ON auth_keyed_receipt_secret
 BEGIN SELECT RAISE(ABORT,'keyed receipt secret is immutable'); END;
CREATE TRIGGER auth_keyed_receipt_secret_no_delete BEFORE DELETE ON auth_keyed_receipt_secret
 BEGIN SELECT RAISE(ABORT,'keyed receipt secret is immutable'); END;
CREATE TRIGGER auth_keyed_receipt_secret_no_replace BEFORE INSERT ON auth_keyed_receipt_secret
 WHEN EXISTS(SELECT 1 FROM auth_keyed_receipt_secret WHERE singleton=NEW.singleton)
 BEGIN SELECT RAISE(ABORT,'keyed receipt secret is immutable'); END;
