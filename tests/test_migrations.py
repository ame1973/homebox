"""Reference migration invariants; domain service guarantees need separate Go tests."""
from pathlib import Path
import sqlite3
from tempfile import TemporaryDirectory
import unittest

ROOT = Path(__file__).resolve().parents[1]
BASELINE = (ROOT / "server/migrations/0001_v07_baseline.sql").read_text()
CANONICAL = (ROOT / "server/migrations/0002_v09_canonical.sql").read_text()
NOW = "2026-11-04T00:00:00Z"


def database():
    db = sqlite3.connect(":memory:")
    db.execute("PRAGMA foreign_keys=ON")
    db.executescript(BASELINE)
    db.executescript(CANONICAL)
    return db


class ReferenceMigrationTests(unittest.TestCase):
    def setUp(self):
        self.db = database()
        self.addCleanup(self.db.close)

    def location(self, identity="location-1", code=None):
        self.db.execute("INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,?,?,?,?)",
                        (identity, code or identity, "Storage", NOW, NOW))
        return identity

    def item(self, identity="item-1", mode="individual"):
        self.db.execute("INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)",
                        (identity, "Camera", mode, NOW, NOW))
        return identity

    def unit(self):
        self.item()
        self.location()
        self.db.execute("INSERT INTO item_units(id,item_id,location_id,asset_code,created_at,updated_at) VALUES(?,?,?,?,?,?)",
                        ("unit-1", "item-1", "location-1", "AST-000001", NOW, NOW))

    def test_fresh_install_has_strict_json_fts_and_valid_relations(self):
        self.assertGreaterEqual(sqlite3.sqlite_version_info, (3, 37))
        self.assertEqual("ok", self.db.execute("PRAGMA integrity_check").fetchone()[0])
        self.assertEqual([], self.db.execute("PRAGMA foreign_key_check").fetchall())
        self.assertEqual([(1,), (2,)], self.db.execute("SELECT version FROM schema_migrations ORDER BY version").fetchall())
        self.assertEqual((1,), self.db.execute("SELECT json_valid('{\"ok\":true}')").fetchone())
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("INSERT INTO data_revision(scope,revision) VALUES('bad','not-an-integer')")

    def test_populated_v07_upgrade_preserves_quantities_and_identifiers(self):
        with sqlite3.connect(":memory:") as old:
            old.executescript(BASELINE)
            old.execute("INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('location-1','BOX-1','Original',?,?)", (NOW, NOW))
            old.execute("INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item-1','Cable','quantity',?,?)", (NOW, NOW))
            old.execute("INSERT INTO stock_balances VALUES('item-1','location-1',1500,?)", (NOW,))
            old.execute("INSERT INTO item_barcodes VALUES('barcode-1','item-1','EAN_13','0012345678905','0012345678905',?)", (NOW,))
            old.commit()
            old.executescript(CANONICAL)
            self.assertEqual(1500, old.execute("SELECT quantity_base FROM stock_balances").fetchone()[0])
            self.assertEqual("0012345678905", old.execute("SELECT normalized_value FROM item_barcodes").fetchone()[0])
            self.assertEqual([], old.execute("PRAGMA foreign_key_check").fetchall())

    def test_location_self_parent_and_duplicate_code_are_rejected(self):
        self.location()
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("UPDATE locations SET parent_id=id")
        with self.assertRaises(sqlite3.IntegrityError):
            self.location("location-2", "location-1")

    def test_duplicate_barcodes_across_items_preserve_leading_zeroes(self):
        for number in [1, 2]:
            self.item(f"item-{number}")
            self.db.execute("INSERT INTO item_barcodes VALUES(?,?,'EAN_13',?,?,?)",
                            (f"barcode-{number}", f"item-{number}", "0012345678905", "0012345678905", NOW))
        self.assertEqual(2, self.db.execute("SELECT COUNT(*) FROM item_barcodes WHERE normalized_value='0012345678905'").fetchone()[0])
        self.db.execute("DELETE FROM item_barcodes WHERE id='barcode-1'")
        self.assertEqual([("barcode-2",)], self.db.execute("SELECT id FROM item_barcodes").fetchall())

    def test_shared_media_requires_exactly_one_real_owner(self):
        self.item()
        self.item("item-2")
        self.location()
        self.db.execute("INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('media-1','object','digest','image/jpeg',100,100,100,?)", (NOW,))
        for number in [1, 2]:
            self.db.execute("INSERT INTO media_links(id,media_id,item_id) VALUES(?,'media-1',?)", (f"link-{number}", f"item-{number}"))
        for query in ["INSERT INTO media_links(id,media_id) VALUES('bad','media-1')",
                      "INSERT INTO media_links(id,media_id,item_id,location_id) VALUES('bad','media-1','item-1','location-1')",
                      "INSERT INTO media_links(id,media_id,item_id) VALUES('bad','media-1','missing')"]:
            with self.subTest(query=query), self.assertRaises(sqlite3.IntegrityError):
                self.db.execute(query)
        self.db.execute("DELETE FROM media_links WHERE id='link-1'")
        self.assertEqual(1, self.db.execute("SELECT COUNT(*) FROM media_links").fetchone()[0])
        self.assertEqual(1, self.db.execute("SELECT COUNT(*) FROM media").fetchone()[0])

    def test_stock_is_nonnegative_integer_and_unique_per_location(self):
        self.item(mode="quantity")
        self.location()
        self.db.execute("INSERT INTO stock_balances VALUES('item-1','location-1',10,?)", (NOW,))
        for value in [-1, 0.5, "text"]:
            with self.subTest(value=value), self.assertRaises(sqlite3.IntegrityError):
                self.db.execute("UPDATE stock_balances SET quantity_base=?", (value,))
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("INSERT INTO stock_balances VALUES('item-1','location-1',1,?)", (NOW,))

    def test_asset_purchase_and_manual_cost_bases_are_exclusive(self):
        self.unit()
        for basis, amount in [("purchase", 100), ("manual", None), ("manual", -1)]:
            with self.subTest(basis=basis, amount=amount), self.assertRaises(sqlite3.IntegrityError):
                self.db.execute("INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,created_at,updated_at) VALUES('unit-1',?,?,'JPY',?,?)", (basis, amount, NOW, NOW))
        self.db.execute("INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,created_at,updated_at) VALUES('unit-1','manual',120000,'JPY',?,?)", (NOW, NOW))

    def test_pending_review_does_not_publish_itself_to_fts(self):
        self.item()
        self.db.execute("INSERT INTO search_documents(item_id,title,updated_at) VALUES('item-1','Camera',?)", (NOW,))
        self.db.execute("INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at) VALUES('review-1','item-1','tag','wrongword','needs_review',?,?)", (NOW, NOW))
        self.assertEqual(0, self.db.execute("SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'wrongword'").fetchone()[0])
        self.db.execute("UPDATE search_documents SET reviewed_ai='curatedword'")
        self.assertEqual(1, self.db.execute("SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'curatedword'").fetchone()[0])
        self.db.execute("UPDATE search_documents SET reviewed_ai='correctedword'")
        self.assertEqual(0, self.db.execute("SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'curatedword'").fetchone()[0])
        self.db.execute("DELETE FROM search_documents")
        self.assertEqual(0, self.db.execute("SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'Camera'").fetchone()[0])

    def test_domain_and_outbox_rollback_together_at_sql_boundary(self):
        self.location()
        self.db.commit()
        with self.assertRaises(sqlite3.IntegrityError):
            with self.db:
                self.db.execute("UPDATE locations SET name='Moved' WHERE id='location-1'")
                self.db.execute("INSERT INTO projection_revisions VALUES('location','location-1',1)")
                self.db.execute("INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location','location-1','upsert',1,'{}',?)", (NOW,))
                self.location("location-2", "location-1")
        self.assertEqual("Storage", self.db.execute("SELECT name FROM locations").fetchone()[0])
        self.assertEqual(0, self.db.execute("SELECT COUNT(*) FROM sync_changes").fetchone()[0])
        self.assertEqual(0, self.db.execute("SELECT COUNT(*) FROM projection_revisions").fetchone()[0])

    def test_outbox_constraints_reject_invalid_tombstones(self):
        for operation, payload in [("delete", "{}"), ("upsert", None), ("invalid", "{}"), ("upsert", "not-json")]:
            with self.subTest(operation=operation, payload=payload), self.assertRaises(sqlite3.IntegrityError):
                self.db.execute("INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('item','item-1',?,1,?,?)", (operation, payload, NOW))

    def test_sqlite_online_backup_captures_committed_wal_data(self):
        with TemporaryDirectory() as folder:
            with sqlite3.connect(Path(folder) / "live.sqlite") as live, sqlite3.connect(Path(folder) / "snapshot.sqlite") as snapshot:
                self.assertEqual("wal", live.execute("PRAGMA journal_mode=WAL").fetchone()[0])
                live.executescript(BASELINE)
                live.executescript(CANONICAL)
                live.execute("INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('location-1','BOX-1','Stored',?,?)", (NOW, NOW))
                live.commit()
                live.backup(snapshot)
                live.execute("UPDATE locations SET name='Later'")
                live.commit()
                self.assertEqual("Stored", snapshot.execute("SELECT name FROM locations").fetchone()[0])
                self.assertEqual("ok", snapshot.execute("PRAGMA integrity_check").fetchone()[0])
                self.assertEqual([], snapshot.execute("PRAGMA foreign_key_check").fetchall())


if __name__ == "__main__":
    unittest.main()
