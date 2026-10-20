package offline

import "testing"

func TestExactDeleteRequestSHA256V2MatchesFleetCanonicalRequest(t *testing.T) {
	const expected = "33156f2ea77a0ba0cef72860fb3c9539326e293ef1becc3f6ae0bdf3a1bc3d3d"
	if got := ExactDeleteRequestSHA256V2(); got != expected {
		t.Fatalf("delete request digest=%s want=%s", got, expected)
	}
}
