package logs

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSummarySuccessSignals(t *testing.T) {
	startedAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(1500 * time.Millisecond)

	summary := BuildSummary(
		"ok",
		"berapa order gagal hari ini",
		startedAt,
		finishedAt,
		2,
		2,
		"Bandingkan count gagal dengan total order hari ini",
		"Ada 12 order gagal hari ini UTC.",
		false,
		nil,
	)

	if summary.Confidence != "high" {
		t.Fatalf("confidence = %q, want high", summary.Confidence)
	}
	if summary.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete", summary.Completeness)
	}
	if summary.RecommendedAction != "none" {
		t.Fatalf("recommended_action = %q, want none", summary.RecommendedAction)
	}
	if summary.DurationMs != 1500 {
		t.Fatalf("duration_ms = %d, want 1500", summary.DurationMs)
	}
	text := summary.Text()
	for _, want := range []string{
		"[run]",
		"status: ok",
		"[assessment]",
		"confidence: high",
		"completeness: complete",
		"recommended_action: none",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary text missing %q\n%s", want, text)
		}
	}
}

func TestBuildSummaryMaxTurnsSignals(t *testing.T) {
	summary := BuildSummary(
		"ok",
		"cek lead kosong",
		time.Now().UTC(),
		time.Now().UTC().Add(2*time.Second),
		4,
		4,
		"Cari tabel yang relevan",
		"Reached max_turns without a final answer. Last steps shown above.",
		true,
		nil,
	)

	if summary.Confidence != "low" {
		t.Fatalf("confidence = %q, want low", summary.Confidence)
	}
	if summary.Completeness != "partial" {
		t.Fatalf("completeness = %q, want partial", summary.Completeness)
	}
	if !strings.Contains(summary.RecommendedAction, "narrower question") {
		t.Fatalf("recommended_action = %q, want rerun guidance", summary.RecommendedAction)
	}
}

func TestBuildSummaryFailureSignals(t *testing.T) {
	err := assertErr("mysql timeout")
	summary := BuildSummary(
		"failed",
		"cek mysql",
		time.Now().UTC(),
		time.Now().UTC().Add(time.Second),
		1,
		1,
		"Jalankan query mysql",
		"",
		false,
		err,
	)

	if summary.Confidence != "low" {
		t.Fatalf("confidence = %q, want low", summary.Confidence)
	}
	if summary.Completeness != "incomplete" {
		t.Fatalf("completeness = %q, want incomplete", summary.Completeness)
	}
	if !strings.Contains(summary.RecommendedAction, "failure.json") {
		t.Fatalf("recommended_action = %q, want failure guidance", summary.RecommendedAction)
	}
	if !strings.Contains(summary.Text(), "error: mysql timeout") {
		t.Fatalf("summary text missing error line\n%s", summary.Text())
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
