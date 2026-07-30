package loadgen

import "testing"

func TestGroupForSequence(t *testing.T) {
	tests := []struct {
		sequence   int
		wantGroup  string
		wantMember int
	}{
		{sequence: 0, wantGroup: "run-gang-000000", wantMember: 0},
		{sequence: 3, wantGroup: "run-gang-000000", wantMember: 3},
		{sequence: 4, wantGroup: "run-gang-000001", wantMember: 0},
	}
	for _, test := range tests {
		group, member := groupForSequence("run", 4, test.sequence)
		if group != test.wantGroup || member != test.wantMember {
			t.Fatalf("sequence %d: got (%q,%d), want (%q,%d)",
				test.sequence, group, member, test.wantGroup, test.wantMember)
		}
	}
}

func TestGroupForSequence_Disabled(t *testing.T) {
	group, member := groupForSequence("", 1, 9)
	if group != "" || member != 0 {
		t.Fatalf("gang disabled: got (%q,%d)", group, member)
	}
}
