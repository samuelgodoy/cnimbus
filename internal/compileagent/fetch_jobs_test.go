package compileagent

import (
	"strconv"
	"testing"
)

func TestBuildJobsHonorsCNIMBUS_JOBSOverride(t *testing.T) {
	t.Setenv("CNIMBUS_JOBS", "3")
	if got := buildJobs(); got != "3" {
		t.Fatalf("expected CNIMBUS_JOBS=3 to win outright, got %q", got)
	}
}

func TestBuildJobsIgnoresInvalidCNIMBUS_JOBS(t *testing.T) {
	t.Setenv("CNIMBUS_JOBS", "not-a-number")
	got := buildJobs()
	if got == "" {
		t.Fatal("expected a fallback value, got empty string")
	}
	if _, err := strconv.Atoi(got); err != nil {
		t.Fatalf("expected buildJobs to fall back to a real number, got %q", got)
	}
}

func TestBuildJobsIgnoresZeroOrNegativeCNIMBUS_JOBS(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		t.Setenv("CNIMBUS_JOBS", v)
		got := buildJobs()
		if got == v {
			t.Fatalf("CNIMBUS_JOBS=%q should not be honored, but buildJobs returned it verbatim", v)
		}
	}
}

func TestGbFloorRoundsDownWithMinimumOfOne(t *testing.T) {
	cases := []struct {
		bytes int64
		want  int
	}{
		{0, 1},
		{512 * 1024 * 1024, 1},                // 0.5 GiB -> floor is 0, clamped to 1
		{1024 * 1024 * 1024, 1},               // exactly 1 GiB
		{3*1024*1024*1024 + 500*1024*1024, 3}, // 3.5 GiB -> floors to 3
	}
	for _, c := range cases {
		if got := gbFloor(c.bytes); got != c.want {
			t.Errorf("gbFloor(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
}
