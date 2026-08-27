package consul

import "testing"

func TestServiceTagValueUsesExactKey(t *testing.T) {
	tags := []string{"norn.region=iad", "norn.node-pool=app", "norn.allocation=alloc-1"}
	if got := serviceTagValue(tags, "norn.region"); got != "iad" {
		t.Fatalf("region = %q", got)
	}
	if got := serviceTagValue(tags, "norn.node"); got != "" {
		t.Fatalf("partial key matched = %q", got)
	}
}
