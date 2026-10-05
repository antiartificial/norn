package lifecycle

import "testing"

func TestCanonicalTargetRules(t *testing.T) {
	t.Parallel()
	canon, id, err := CanonicalTarget(TargetIdentity{
		Provider: " AWS ", ProviderAccount: " 123456789012 ", StateBackend: "S3://Bucket-Name/Path/To/State/",
	})
	if err != nil {
		t.Fatalf("expected a valid identity to canonicalize, got %v", err)
	}
	if canon.Provider != "aws" || canon.ProviderAccount != "123456789012" {
		t.Fatalf("provider/account must be lowercased and trimmed, got %+v", canon)
	}
	if canon.StateBackend != "s3://bucket-name/Path/To/State" {
		t.Fatalf("scheme and host must be lowercased, path case kept, trailing slash removed, got %q", canon.StateBackend)
	}
	if id == "" || id[:4] != "tgt_" {
		t.Fatalf("expected a tgt_ prefixed ID, got %q", id)
	}

	// Canonicalization is deterministic: the same identity, differently
	// cased and with a trailing slash, produces the same ID.
	_, id2, err := CanonicalTarget(TargetIdentity{
		Provider: "aws", ProviderAccount: "123456789012", StateBackend: "s3://bucket-name/Path/To/State",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("equivalent identities must derive the same target ID: %q != %q", id, id2)
	}

	// An encoded "/" in the path must not collide with a literal "/".
	_, encoded, err := CanonicalTarget(TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "s3://b/x%2Fy/"})
	if err != nil {
		t.Fatal(err)
	}
	_, literal, err := CanonicalTarget(TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "s3://b/x/y"})
	if err != nil {
		t.Fatal(err)
	}
	if encoded == literal {
		t.Fatal("an encoded slash and a literal slash must derive different target IDs")
	}

	cases := []struct {
		name     string
		identity TargetIdentity
	}{
		{"missing provider", TargetIdentity{ProviderAccount: "a", StateBackend: "https://host/path"}},
		{"missing account", TargetIdentity{Provider: "aws", StateBackend: "https://host/path"}},
		{"missing backend", TargetIdentity{Provider: "aws", ProviderAccount: "a"}},
		{"relative backend", TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "/just/a/path"}},
		{"no host", TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "file:///local/path"}},
		{"userinfo", TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "https://user:pass@host/path"}},
		{"query", TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "https://host/path?x=1"}},
		{"fragment", TargetIdentity{Provider: "aws", ProviderAccount: "a", StateBackend: "https://host/path#frag"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := CanonicalTarget(tc.identity); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestParseAlias(t *testing.T) {
	t.Parallel()
	if kind, value, ok := ParseAlias("cluster:prod-us"); !ok || kind != AliasKindCluster || value != "prod-us" {
		t.Fatalf("expected a valid cluster alias, got kind=%q value=%q ok=%v", kind, value, ok)
	}
	if kind, value, ok := ParseAlias("environment:prod"); !ok || kind != AliasKindEnvironment || value != "prod" {
		t.Fatalf("expected a valid environment alias, got kind=%q value=%q ok=%v", kind, value, ok)
	}
	if _, _, ok := ParseAlias("root:anything"); ok {
		t.Fatal("the root: alias kind is dropped (m6) and must not parse as valid")
	}
	if _, _, ok := ParseAlias("cluster:"); ok {
		t.Fatal("an empty alias value must not parse as valid")
	}
	if _, _, ok := ParseAlias("no-colon"); ok {
		t.Fatal("an alias with no kind separator must not parse as valid")
	}
}
