package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLocationSignedPageHasScopedOpaqueContinuationAndPublicFieldsOnly(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	for _, code := range []string{"FIRST", "SECOND", "THIRD"} {
		createLocation(t, locations, token, code, nil)
	}
	first, err := locations.Page(t.Context(), token, "", 2)
	if err != nil || len(first.Data) != 2 || first.NextCursor == nil {
		t.Fatal("first signed page failed", err)
	}
	second, err := locations.Page(t.Context(), token, *first.NextCursor, 2)
	if err != nil || len(second.Data) != 1 || second.NextCursor != nil || second.Data[0].ID <= first.Data[1].ID {
		t.Fatal("signed continuation did not retain stable ordering", err)
	}
	body, err := json.Marshal(first)
	if err != nil || strings.Contains(string(body), "NextID") || strings.Contains(string(body), token) || strings.Contains(string(body), testAdminPassword) {
		t.Fatal("list wire data exposed internal state or a credential")
	}
	if _, err = locations.Page(t.Context(), token, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("cursor moved to a different page-size scope", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err = locations.Page(t.Context(), token, "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded signed page was accepted", err)
		}
	}
}

func TestLocationCursorRejectsTamperingAndNoncanonicalEncodings(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	createLocation(t, locations, token, "FIRST", nil)
	createLocation(t, locations, token, "SECOND", nil)
	first, err := locations.Page(t.Context(), token, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(*first.NextCursor, ".")
	for _, cursor := range []string{
		"unsigned-row-id", ".", "." + parts[1], parts[0] + ".", *first.NextCursor + ".extra",
		parts[0] + "=." + parts[1], parts[0] + "." + parts[1] + "=", "!invalid." + parts[1],
		parts[0] + "." + strings.Repeat("!", 43), parts[0] + "." + strings.Repeat("A", 43),
		base64.RawURLEncoding.EncodeToString([]byte(`{"after":"forged"}`)) + "." + parts[1],
	} {
		if _, err = locations.Page(t.Context(), token, cursor, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("tampered cursor was accepted", err)
		}
	}
	if _, err = locations.Page(t.Context(), token, strings.Repeat("x", 2049), 1); !errors.Is(err, ErrDomainInput) {
		t.Fatal("oversized cursor was admitted", err)
	}
}

func TestLocationCursorSurvivesFreshLoginButNeverAuthorizesItself(t *testing.T) {
	_, locations, a, token := locationFixture(t)
	createLocation(t, locations, token, "FIRST", nil)
	createLocation(t, locations, token, "SECOND", nil)
	first, err := locations.Page(t.Context(), token, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Logout(t.Context(), token, "logout-page-session"); err != nil {
		t.Fatal(err)
	}
	if _, err = locations.Page(t.Context(), token, *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
		t.Fatal("cursor reactivated a revoked session", err)
	}
	if _, err = locations.Page(t.Context(), *first.NextCursor, "", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatal("cursor was accepted as a session credential", err)
	}
	_, fresh := login(t, a)
	last, err := locations.Page(t.Context(), fresh, *first.NextCursor, 1)
	if err != nil || len(last.Data) != 1 || last.Data[0].ID <= first.Data[0].ID {
		t.Fatal("fresh session could not continue the same principal's read", err)
	}
}

func TestLocationCursorExpiryAndDatasetResetFailClosed(t *testing.T) {
	for _, state := range []string{"expired", "epoch", "mandatory"} {
		t.Run(state, func(t *testing.T) {
			s, a, now, _, token := completedAuthFixture(t)
			locations, err := NewLocations(a)
			if err != nil {
				t.Fatal(err)
			}
			createLocation(t, locations, token, "FIRST", nil)
			createLocation(t, locations, token, "SECOND", nil)
			first, err := locations.Page(t.Context(), token, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			want := ErrInvalidCursor
			switch state {
			case "expired":
				*now = now.Add(metadataCursorLifetime)
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
				if _, err = locations.Page(t.Context(), token, *first.NextCursor, 1); !errors.Is(err, ErrAuthentication) {
					t.Fatal("old epoch cookie authenticated", err)
				}
				_, token = login(t, a)
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			}
			if _, err = locations.Page(t.Context(), token, *first.NextCursor, 1); !errors.Is(err, want) {
				t.Fatal("stale or unauthorized cursor read was admitted", err)
			}
		})
	}
}

func TestLocationCursorPrivateKeyAndPayloadScopesAreIndependent(t *testing.T) {
	s, locations, a, token := locationFixture(t)
	session, err := a.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	now := a.now()
	epoch := scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state")
	valid := metadataCursor{AdminID: session.Admin.ID, Epoch: epoch, AfterID: "location", Limit: 10, Issued: now.Unix(), Expires: now.Add(metadataCursorLifetime).Unix()}
	encoded, err := encodeMetadataCursor(locations.auth, locationCursorPurpose, valid)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := decodeMetadataCursor(locations.auth, locationCursorPurpose, encoded, session.Admin.ID, epoch, 10, now); err != nil || !reflect.DeepEqual(value, valid) {
		t.Fatal("valid signed payload failed", err)
	}
	_, other, _, _ := locationFixture(t)
	if _, err = decodeMetadataCursor(other.auth, locationCursorPurpose, encoded, session.Admin.ID, epoch, 10, now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("a different server key accepted the cursor", err)
	}
	for _, change := range []func(*metadataCursor){
		func(v *metadataCursor) { v.AdminID = "another-principal" }, func(v *metadataCursor) { v.Epoch = "another-epoch" },
		func(v *metadataCursor) { v.AfterID = "" }, func(v *metadataCursor) { v.Issued = now.Unix() + 1; v.Expires++ },
		func(v *metadataCursor) { v.Issued = -1 }, func(v *metadataCursor) { v.Expires = v.Issued },
		func(v *metadataCursor) { v.Expires++ },
	} {
		value := valid
		change(&value)
		cursor, err := encodeMetadataCursor(locations.auth, locationCursorPurpose, value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decodeMetadataCursor(locations.auth, locationCursorPurpose, cursor, session.Admin.ID, epoch, 10, now); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("incorrectly scoped server-signed cursor was accepted", err)
		}
	}
	// The namespace is independent of all credential/capability MAC purposes.
	body, _ := json.Marshal(valid)
	for _, purpose := range []string{"csrf-token-v1", "reauth-token-v2", "auth-request-v2"} {
		wrong := base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(a.authMAC("", purpose, body))
		if _, err = decodeMetadataCursor(locations.auth, locationCursorPurpose, wrong, session.Admin.ID, epoch, 10, now); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("credential MAC namespace collided with a list cursor", err)
		}
	}
	for _, body := range []string{`[]`, `{"unknown":true}`, `not-json`, string(body) + `{}`} {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(body)) + "." + base64.RawURLEncoding.EncodeToString(a.authMAC("", "locations-tree-cursor-v1", []byte(body)))
		if _, err = decodeMetadataCursor(locations.auth, locationCursorPurpose, cursor, session.Admin.ID, epoch, 10, now); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("invalid server-signed payload was accepted", err)
		}
	}
	a.serverSecret = nil
	if _, err = encodeMetadataCursor(locations.auth, locationCursorPurpose, valid); !errors.Is(err, ErrDomainState) {
		t.Fatal("missing private signer key did not fail closed", err)
	}
	if _, err = decodeMetadataCursor(locations.auth, locationCursorPurpose, encoded, session.Admin.ID, epoch, 10, now); !errors.Is(err, ErrDomainState) {
		t.Fatal("missing private verifier key did not fail closed", err)
	}
}

func TestLocationSignedListIsLiveNotAFakeFrozenSnapshot(t *testing.T) {
	_, locations, _, token := locationFixture(t)
	for _, code := range []string{"FIRST", "SECOND", "THIRD"} {
		createLocation(t, locations, token, code, nil)
	}
	all, err := locations.Page(t.Context(), token, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	first, err := locations.Page(t.Context(), token, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	last := all.Data[2]
	if _, err = locations.Delete(t.Context(), token, "delete-between-pages", last.ID, last.Revision); err != nil {
		t.Fatal(err)
	}
	second, err := locations.Page(t.Context(), token, *first.NextCursor, 1)
	if err != nil || len(second.Data) != 1 || second.NextCursor != nil || second.Data[0].ID != all.Data[1].ID {
		t.Fatal("ordinary metadata pagination falsely retained deleted snapshot data", err)
	}
}

func TestLocationStoreBinderRejectsUninitializedAuthentication(t *testing.T) {
	for _, auth := range []*Auth{nil, {}} {
		if _, err := NewLocations(auth); !errors.Is(err, ErrDomainInput) {
			t.Fatal("HTTP/module binder accepted an uninitialized auth dependency", err)
		}
	}
}
