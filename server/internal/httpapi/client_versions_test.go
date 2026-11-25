package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func TestWriteClientVersionsMatchActualAdmission(t *testing.T) {
	versions := supportedWriteClientVersions()
	if !reflect.DeepEqual(versions, []string{"0.1.0", "0.1.0-dev"}) {
		t.Fatal("advertised versions must preserve the exact existing write policy", versions)
	}
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	api := &authAPI{auth: auth}
	for _, version := range []string{"0.1.0", "0.1.0-dev", "0.0.9", "0.1.1", "0.2.0", "", " 0.1.0", "0.1.0-dev+other"} {
		r := writeRequest("/api/v1/items", "{}", cookie, csrf, "version-policy-key")
		r.Header.Set("X-HomeBox-Client-Version", version)
		response := httptest.NewRecorder()
		_, _, admitted := api.writeCredentials(response, r, "version-policy-request")
		if admitted != slices.Contains(versions, version) {
			t.Fatalf("write policy and advertised versions disagree for %q", version)
		}
		if !admitted && response.Code != http.StatusUpgradeRequired {
			t.Fatalf("unsupported version %q returned %d instead of 426", version, response.Code)
		}
	}
	r := writeRequest("/api/v1/items", "{}", cookie, csrf, "version-policy-key")
	r.Header.Add("X-HomeBox-Client-Version", versions[0])
	response := httptest.NewRecorder()
	if _, _, admitted := api.writeCredentials(response, r, "version-policy-request"); admitted || response.Code != http.StatusUpgradeRequired {
		t.Fatal("duplicate version headers must not be admitted")
	}
	versions[0] = "9.9.9"
	if actual := supportedWriteClientVersions(); !reflect.DeepEqual(actual, []string{"0.1.0", "0.1.0-dev"}) {
		t.Fatal("caller mutation changed the write policy", actual)
	}
}
