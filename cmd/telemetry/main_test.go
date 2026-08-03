package main

import "testing"

func TestBuildPromQueriesAddsUniqueCustomQuery(t *testing.T) {
	queries, err := buildPromQueries([]string{"scheduler_queue=sum(scheduler_pending_pods)"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(queries), 4; got != want {
		t.Fatalf("got %d queries, want %d", got, want)
	}
	if _, err := buildPromQueries([]string{"apf_inqueue=up"}); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestOutputPathsMatchAnalysisConvention(t *testing.T) {
	got := outputPaths("results/run")
	if got.meta != "results/run-meta.json" ||
		got.host != "results/run-host.csv" ||
		got.apiserver != "results/run-apiserver.csv" ||
		got.pressure != "results/run-pressure.csv" {
		t.Fatalf("unexpected paths: %#v", got)
	}
}
