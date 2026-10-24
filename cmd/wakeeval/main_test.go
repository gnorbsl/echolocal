package main

import "testing"

func TestSummarizeSplitsTestPromptsFromTheRest(t *testing.T) {
	// One score per 20 ms frame, 20 s long.
	scores := make([]float64, 1000)
	at := func(sec float64) int { return int(sec * 50) }
	scores[at(2.5)] = 0.95  // answer to prompt 1, a training prompt
	scores[at(6.5)] = 0.70  // answer to prompt 2, a test prompt
	scores[at(10.5)] = 0.85 // answer to prompt 3, a test prompt
	scores[at(15.0)] = 0.90 // far from every prompt: a false wake
	scores[at(15.4)] = 0.92 // same false wake, still within a second

	prompts := []prompt{{N: 1, AtSec: 2}, {N: 2, AtSec: 6}, {N: 3, AtSec: 10}}
	r := summarize(scores, prompts, 2, 3, []float64{0.6, 0.8})

	if len(r.peaks) != 2 || r.peaks[0] != 0.70 || r.peaks[1] != 0.85 {
		t.Fatalf("test peaks %v, want [0.70 0.85]", r.peaks)
	}
	if r.heard[0] != 2 || r.heard[1] != 1 {
		t.Fatalf("heard %v at cutoffs 0.6 and 0.8, want [2 1]", r.heard)
	}
	if r.wakes[0] != 1 || r.wakes[1] != 1 {
		t.Fatalf("wakes outside prompts %v, want [1 1]", r.wakes)
	}
	if len(r.wakesAt[0]) != 1 || r.wakesAt[0][0] != 15.0 {
		t.Fatalf("wakes at %v, want [15] s at the first cutoff", r.wakesAt[0])
	}
	// 20 s minus three 3-second prompt windows.
	if r.outsideSec < 10.9 || r.outsideSec > 11.1 {
		t.Fatalf("outside prompts %.2f s, want 11", r.outsideSec)
	}
}

func TestSummarizeWithoutPromptsCountsEverythingAsOutside(t *testing.T) {
	scores := make([]float64, 500)
	scores[100] = 0.99
	r := summarize(scores, nil, 41, 50, []float64{0.8})
	if r.wakes[0] != 1 || r.outsideSec != 10 || len(r.peaks) != 0 {
		t.Fatalf("got %+v, want one wake in 10 s and no test prompts", r)
	}
}
