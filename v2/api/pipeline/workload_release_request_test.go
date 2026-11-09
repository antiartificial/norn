package pipeline

import "testing"

func TestWorkloadReleaseRequestDigestIsStableAndBindsSelection(t *testing.T) {
	first, err := WorkloadReleaseRequestDigest("qualification-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkloadReleaseRequestDigest("qualification-1", 3)
	if err != nil || first != second {
		t.Fatalf("same selection changed digest: first=%q second=%q err=%v", first, second, err)
	}
	otherQualification, err := WorkloadReleaseRequestDigest("qualification-2", 3)
	if err != nil || first == otherQualification {
		t.Fatalf("qualification selection was not bound: other=%q err=%v", otherQualification, err)
	}
	otherReplicas, err := WorkloadReleaseRequestDigest("qualification-1", 4)
	if err != nil || first == otherReplicas {
		t.Fatalf("replica selection was not bound: other=%q err=%v", otherReplicas, err)
	}
}

func TestWorkloadReleaseRequestDigestRejectsIncompleteSelection(t *testing.T) {
	for _, input := range []struct {
		qualification string
		replicas      int
	}{
		{"", 1},
		{" qualification-1", 1},
		{"qualification-1 ", 1},
		{"qualification-1", 0},
		{"qualification-1", -1},
	} {
		if digest, err := WorkloadReleaseRequestDigest(input.qualification, input.replicas); err == nil || digest != "" {
			t.Errorf("invalid selection (%q, %d) produced digest %q, err=%v", input.qualification, input.replicas, digest, err)
		}
	}
}
