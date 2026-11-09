"""Shared-media wire guarantees; schemas do not establish file or HTTP behavior."""
import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from tests.test_contracts import API, PROJECTION, SCHEMAS, record, validator


MAXIMUM = 9007199254740991
MEDIA = {"id": "media-1", "revision": 7, "sha256": "a" * 64, "mime_type": "image/jpeg",
         "width": 4000, "height": 3000, "bytes": 123456,
         "thumb_url": "/api/v1/media/media-1/thumbnail", "thumb_sha256": "b" * 64}
LINK = {"id": "link-1", "revision": 3, "media_id": "media-1", "owner_type": "item",
        "owner_id": "item-1", "is_cover": True, "sort_order": -1, "description": "  Exact é\n"}
LINK_VIEW = {**LINK, "media": MEDIA}
OWNERS = {"item_id": "item-1", "item_unit_id": "unit-1", "location_id": "location-1"}


class MediaContractTests(unittest.TestCase):
    def reject(self, name, *values):
        for value in values:
            with self.subTest(schema=name, value=value), self.assertRaises(ValidationError):
                validator(name).validate(value)

    def test_media_and_links_keep_independent_complete_bounded_identities(self):
        validator("Media").validate(MEDIA)
        validator("Media").validate({**MEDIA, "thumb_url": None, "thumb_sha256": None})
        validator("MediaLink").validate(LINK)
        validator("MediaLinkView").validate(LINK_VIEW)
        self.reject("MediaLink", LINK_VIEW)
        self.reject("MediaLinkView", LINK, {**LINK_VIEW, "private_path": "/private"})
        for name, value in [("Media", MEDIA), ("MediaLink", LINK), ("MediaLinkView", LINK_VIEW)]:
            for field in value:
                self.reject(name, {key: item for key, item in value.items() if key != field})
            for patch in [{"id": ""}, {"id": "😀" * 129}, {"revision": 0}, {"revision": -1},
                          {"revision": 1.5}, {"revision": MAXIMUM + 1}, {"private_path": "/private"}]:
                self.reject(name, {**value, **patch})
            validator(name).validate({**value, "id": "😀" * 128, "revision": MAXIMUM})
        self.reject("MediaLink", {**LINK, "media_id": ""}, {**LINK, "owner_id": "😀" * 129},
                    {**LINK, "owner_type": "purchase"}, {**LINK, "is_cover": 1},
                    {**LINK, "sort_order": MAXIMUM + 1}, {**LINK, "sort_order": -MAXIMUM - 1},
                    {**LINK, "description": "😀" * 10001})
        self.reject("MediaLinkView", {**LINK_VIEW, "media": {"id": "media-1"}})
        validator("MediaLink").validate({**LINK, "sort_order": -MAXIMUM, "description": "😀" * 10000})

    def test_media_representation_requires_safe_actual_digest_and_paired_verified_thumbnail(self):
        for mime in ["image/jpeg", "image/png", "image/webp"]:
            validator("Media").validate({**MEDIA, "mime_type": mime})
        for patch in [{"sha256": "a" * 63}, {"sha256": "A" * 64}, {"sha256": "a" * 64 + "\n"},
                      {"mime_type": "image/svg+xml"}, {"width": 0}, {"height": MAXIMUM + 1},
                      {"bytes": -1}, {"width": True}, {"thumb_url": None}, {"thumb_sha256": None},
                      {"thumb_url": "https://attacker.invalid/thumbnail"}, {"thumb_url": "//attacker.invalid/a"},
                      {"thumb_url": "/api/v1/media/media-1/thumbnail?secret=x"}, {"thumb_sha256": "b" * 63}]:
            self.reject("Media", {**MEDIA, **patch})
        validator("Media").validate({**MEDIA, "width": MAXIMUM, "height": MAXIMUM, "bytes": MAXIMUM})

    def test_link_creation_requires_one_real_owner_and_its_original_proof(self):
        for field, owner in OWNERS.items():
            validator("MediaLinkCreate").validate({field: owner, "owner_revision": 7})
            validator("MediaLinkCreate").validate({field: "😀" * 128, "owner_revision": MAXIMUM,
                                                   "is_cover": False, "sort_order": 0, "description": ""})
            self.reject("MediaLinkCreate", {field: owner}, {field: "", "owner_revision": 7},
                        {field: "😀" * 129, "owner_revision": 7}, {field: owner, "owner_revision": 0},
                        {field: owner, "owner_revision": MAXIMUM + 1}, {field: owner, "owner_revision": None})
        for body in [{}, {"owner_revision": 1}, {"item_id": "item-1", "location_id": "location-1", "owner_revision": 7},
                     {"item_id": "item-1", "item_unit_id": "unit-1", "owner_revision": 7},
                     {"item_id": "item-1", "owner_revision": 7, "sort_order": -1},
                     {"item_id": "item-1", "owner_revision": 7, "sort_order": MAXIMUM + 1},
                     {"item_id": "item-1", "owner_revision": 7, "media_id": "other"},
                     {"item_id": "item-1", "owner_revision": 7, "id": "caller-id"}]:
            self.reject("MediaLinkCreate", body)

    def test_link_patch_is_partial_but_cannot_reassign_or_silently_repair_a_retained_position(self):
        for body in [{"is_cover": False}, {"sort_order": 0}, {"sort_order": MAXIMUM},
                     {"description": ""}, {"description": "😀" * 10000}]:
            validator("MediaLinkWrite").validate(body)
        for body in [{}, {"sort_order": -1}, {"sort_order": MAXIMUM + 1}, {"sort_order": 1.5},
                     {"is_cover": None}, {"description": None}, {"description": "😀" * 10001},
                     {"item_id": "another"}, {"media_id": "another"}, {"owner_revision": 7}]:
            self.reject("MediaLinkWrite", body)

    def test_live_owner_and_media_pages_are_complete_bounded_and_have_no_etag(self):
        validator("MediaLinkList").validate({"data": [], "next_cursor": None})
        validator("MediaLinkList").validate({"data": [LINK_VIEW] * 100, "next_cursor": "x" * 2048})
        for page in [{"data": []}, {"data": [], "next_cursor": ""}, {"data": [], "next_cursor": "x" * 2049},
                     {"data": [LINK_VIEW] * 101, "next_cursor": None}, {"data": [LINK], "next_cursor": None},
                     {"data": [{"id": "link-1"}], "next_cursor": None},
                     {"data": [], "next_cursor": None, "revision": 1}]:
            self.reject("MediaLinkList", page)
        for path in ["/media/links", "/media/{id}/links"]:
            operation = API["paths"][path]["get"]
            parameters = {entry["name"]: entry for entry in operation["parameters"]}
            self.assertEqual(50, parameters["limit"]["schema"]["default"])
            self.assertEqual(100, parameters["limit"]["schema"]["maximum"])
            self.assertEqual(2048, parameters["cursor"]["schema"]["maxLength"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(16384, operation["x-max-query-bytes"])
            self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
            self.assertEqual("#/components/schemas/MediaLinkList", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
            for word in ["principal", "epoch", "lookahead", "live", "15 minutes"]:
                self.assertIn(word, operation["description"])
        owner = {entry["name"]: entry for entry in API["paths"]["/media/links"]["get"]["parameters"]}
        self.assertTrue(owner["owner_type"]["required"] and owner["owner_id"]["required"])
        self.assertEqual({"item", "unit", "location"}, set(owner["owner_type"]["schema"]["enum"]))

    def test_legacy_links_id_has_an_explicit_metadata_escape_and_original_paths_remain(self):
        for path, operation_id in [("/media/{id}", "get_media_id"), ("/media/{id}/view", "get_media_id_view")]:
            operation = API["paths"][path]["get"]
            self.assertEqual(operation_id, operation["operationId"])
            self.assertEqual(0, operation["x-max-body-bytes"])
            self.assertEqual(0, operation["x-max-query-bytes"])
            self.assertEqual("media", operation["x-response-revision-scope"])
            self.assertEqual("#/components/schemas/Media", operation["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
            self.assertEqual({"Cache-Control", "ETag"}, set(operation["responses"]["200"]["headers"]))
        self.assertEqual("get_media_id_links", API["paths"]["/media/{id}/links"]["get"]["operationId"])
        self.assertEqual(144, sum(len(methods) for methods in API["paths"].values()))

    def test_protected_mutations_use_original_media_or_independent_link_proofs(self):
        cases = [("/media", "post", None, 33619968), ("/media/{id}", "delete", "media", 0),
                 ("/media/{id}/links", "post", "media", 131072),
                 ("/media/links/{id}", "patch", "media_link", 131072),
                 ("/media/links/{id}", "delete", "media_link", 0)]
        for path, method, scope, limit in cases:
            operation = API["paths"][path][method]
            headers = {entry["name"]: entry for entry in operation["parameters"] if entry["in"] == "header"}
            self.assertTrue({"Origin", "X-CSRF-Token", "Idempotency-Key", "X-HomeBox-Client-Version"}.issubset(headers))
            self.assertTrue(all(entry["required"] for entry in headers.values()))
            self.assertEqual(limit, operation["x-max-body-bytes"])
            self.assertEqual(0, operation["x-max-query-bytes"])
            if scope is None:
                self.assertNotIn("If-Match", headers)
            else:
                self.assertEqual(scope, operation["x-revision-scope"])
                proof = Draft202012Validator(headers["If-Match"]["schema"])
                for valid in ['"1"', '"9007199254740991"']:
                    proof.validate(valid)
                for invalid in ['1', '"0"', '"01"', '*', 'W/"1"', '"1", "2"', '"1"\n']:
                    with self.assertRaises(ValidationError):
                        proof.validate(invalid)
            if method == "delete":
                self.assertNotIn("requestBody", operation)
                self.assertNotIn("ETag", operation["responses"]["200"]["headers"])
        create = API["paths"]["/media/{id}/links"]["post"]
        self.assertEqual("media_link", create["x-response-revision-scope"])
        for word in ["owner_revision", "original", "inside SQL", "membership", "outbox", "audit", "frozen"]:
            self.assertIn(word, create["description"])
        for word in ["membership", "tombstone", "cleanup", "original-key"]:
            self.assertIn(word, API["paths"]["/media/{id}"]["delete"]["description"])

    def test_upload_and_streams_make_private_file_guards_explicit(self):
        operation = API["paths"]["/media"]["post"]
        self.assertEqual(33554432, operation["x-max-file-bytes"])
        self.assertEqual(64000000, operation["x-max-image-pixels"])
        self.assertEqual(16384, operation["x-max-image-dimension"])
        self.assertEqual({"multipart/form-data"}, set(operation["requestBody"]["content"]))
        self.assertEqual("image/jpeg, image/png, image/webp", operation["requestBody"]["content"]["multipart/form-data"]["encoding"]["file"]["contentType"])
        validator("MediaUploadInput").validate({"file": "binary form part"})
        self.reject("MediaUploadInput", {}, {"file": "binary form part", "sha256": "a" * 64},
                    {"file": "binary form part", "item_id": "item-1"})
        for word in ["magic", "MIME", "pixel", "staging", "digest", "GPS", "recovery", "inside SQL", "server-keyed", "exact raw file bytes", "every accepted animation frame"]:
            self.assertIn(word, operation["description"])
        for path, types in [("/media/{id}/content", {"image/jpeg", "image/png", "image/webp"}),
                            ("/media/{id}/thumbnail", {"image/jpeg"})]:
            read = API["paths"][path]["get"]
            self.assertEqual(types, set(read["responses"]["200"]["content"]))
            self.assertEqual("nosniff", read["responses"]["200"]["headers"]["X-Content-Type-Options"]["schema"]["const"])
            self.assertEqual(0, read["x-max-body-bytes"])
            self.assertEqual(0, read["x-max-query-bytes"])
            self.assertIn("authenticated", read["description"])
            self.assertIn("no-store", read["description"])

    def test_resource_etags_are_independent_and_deletion_ack_is_not_a_file_cleanup_claim(self):
        for path, method in [("/media", "post"), ("/media/{id}", "get"), ("/media/{id}/view", "get"),
                             ("/media/{id}/links", "post"), ("/media/links/{id}", "patch"),
                             ("/media/{id}/content", "get"), ("/media/{id}/thumbnail", "get")]:
            check = Draft202012Validator(API["paths"][path][method]["responses"]["200"]["headers"]["ETag"]["schema"])
            for valid in ['"1"', '"9007199254740991"']:
                check.validate(valid)
            for invalid in ['1', '"0"', '"01"', '*', 'W/"1"', '"1", "2"', '"1"\n', '"12345678901234567"']:
                with self.subTest(path=path, method=method, etag=invalid), self.assertRaises(ValidationError):
                    check.validate(invalid)
        self.assertEqual("#/components/schemas/MediaLinkView", SCHEMAS["MediaLinkList"]["properties"]["data"]["items"]["$ref"])
        self.assertNotIn("media", SCHEMAS["MediaLink"]["properties"])
        for path, method in [("/media/{id}/links", "post"), ("/media/links/{id}", "patch")]:
            self.assertEqual("#/components/schemas/MediaLink", API["paths"][path][method]["responses"]["200"]["content"]["application/json"]["schema"]["$ref"])
        validator("MediaDeleteResult").validate({"id": "media-1", "deleted": True})
        for invalid in [{"id": "", "deleted": True}, {"id": "media-1", "deleted": False},
                        {"id": "media-1", "deleted": True, "file_removed": True}]:
            self.reject("MediaDeleteResult", invalid)
        self.assertIn("does not claim", SCHEMAS["MediaDeleteResult"]["description"])

    def test_schemas_do_not_claim_cross_resource_identity_or_actual_byte_validation(self):
        # A schema cannot compare the link's media_id to its nested Media or read
        # file bytes. The future runtime must reject both mismatches itself.
        validator("MediaLinkView").validate({**LINK_VIEW, "media_id": "different-media"})
        validator("Media").validate({**MEDIA, "width": MAXIMUM, "height": MAXIMUM, "bytes": MAXIMUM})
        self.assertEqual("media-actual-bytes-membership-thumbnail-v1", SCHEMAS["Media"]["x-runtime-validation"])
        self.assertIn("verified by the runtime", SCHEMAS["MediaLinkView"]["description"])

    def test_existing_offline_media_payloads_remain_separate_from_http_embedding(self):
        media_payload = {key: value for key, value in MEDIA.items() if key != "revision"}
        media_payload["perceptual_hash"] = None
        link_payload = {key: value for key, value in LINK.items() if key not in {"revision", "media"}}
        Draft202012Validator(PROJECTION).validate(record("media", media_payload, identity=MEDIA["id"]))
        Draft202012Validator(PROJECTION).validate(record("media_link", link_payload, identity=LINK["id"]))
        self.assertEqual({"id", "media_id", "owner_type", "owner_id", "is_cover", "sort_order", "description"},
                         set(PROJECTION["$defs"]["media_link"]["required"]))
        self.assertNotIn("media", PROJECTION["$defs"]["media_link"]["properties"])
        self.assertIn("MEDIA_COVER_CONFLICT", API["components"]["responses"]["Error409"]["description"])


if __name__ == "__main__":
    unittest.main()
