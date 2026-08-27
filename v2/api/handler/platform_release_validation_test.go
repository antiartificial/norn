package handler

import "testing"

func TestReleaseSHAPrefixPattern(t *testing.T) {
	t.Parallel()
	valid := []string{
		"0123456",
		"0123456789abcdef0123456789abcdef01234567",
	}
	for _, value := range valid {
		if !releaseSHAPrefixPattern.MatchString(value) {
			t.Errorf("expected %q to be accepted", value)
		}
	}

	invalid := []string{
		"",
		"abcdef",
		"ABCDEF0",
		"../outside",
		"a/../../outside",
		"0123456789abcdef0123456789abcdef012345678",
	}
	for _, value := range invalid {
		if releaseSHAPrefixPattern.MatchString(value) {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}
