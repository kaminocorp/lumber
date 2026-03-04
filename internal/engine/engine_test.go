package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/engine/classifier"
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/engine/taxonomy"
	"github.com/kaminocorp/lumber/internal/engine/testdata"
	"github.com/kaminocorp/lumber/internal/model"
)

const (
	modelPath      = "../../models/model_quantized.onnx"
	vocabPath      = "../../models/vocab.txt"
	projectionPath = "../../models/2_Dense/model.safetensors"
)

func skipWithoutModel(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		t.Skip("ONNX model not available, skipping integration test")
	}
}

// newTestEngine creates a fully wired engine with the real embedder, taxonomy,
// classifier, and compactor. Intended for integration tests only.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	skipWithoutModel(t)

	emb, err := embedder.New(modelPath, vocabPath, projectionPath)
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	t.Cleanup(func() { emb.Close() })

	tax, err := taxonomy.New(taxonomy.DefaultRoots(), emb)
	if err != nil {
		t.Fatalf("failed to create taxonomy: %v", err)
	}

	cls := classifier.New(0.5)
	cmp := compactor.New(compactor.Standard)

	return New(emb, tax, cls, cmp)
}

func TestProcessSingleLog(t *testing.T) {
	eng := newTestEngine(t)

	ts := time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)
	raw := model.RawLog{
		Timestamp: ts,
		Source:    "test",
		Raw:       "ERROR [2026-02-19 12:00:00] UserService — connection refused (host=db-primary, port=5432)",
	}

	event, err := eng.Process(raw)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if event.Type == "" {
		t.Error("Type is empty")
	}
	if event.Category == "" {
		t.Error("Category is empty")
	}
	if event.Severity == "" {
		t.Error("Severity is empty")
	}
	if event.Confidence <= 0 {
		t.Errorf("Confidence = %f, want > 0", event.Confidence)
	}
	if event.Summary == "" {
		t.Error("Summary is empty")
	}
	if event.Raw == "" {
		t.Error("Raw is empty (should be preserved at Standard verbosity)")
	}
	if !event.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", event.Timestamp, ts)
	}

	t.Logf("Result: %s.%s (confidence=%.3f, severity=%s)", event.Type, event.Category, event.Confidence, event.Severity)
}

func TestProcessBatchConsistency(t *testing.T) {
	eng := newTestEngine(t)

	raws := []model.RawLog{
		{Raw: "ERROR dial tcp 10.0.0.5:6379: connection refused", Timestamp: time.Now()},
		{Raw: "INFO HTTP 200 OK — GET /api/users responded in 12ms", Timestamp: time.Now()},
		{Raw: "WARN Slow query: SELECT * FROM orders took 8.2s", Timestamp: time.Now()},
	}

	// Process individually.
	singles := make([]model.CanonicalEvent, len(raws))
	for i, raw := range raws {
		event, err := eng.Process(raw)
		if err != nil {
			t.Fatalf("Process(%d) error: %v", i, err)
		}
		singles[i] = event
	}

	// Process as batch.
	batched, err := eng.ProcessBatch(raws)
	if err != nil {
		t.Fatalf("ProcessBatch() error: %v", err)
	}

	if len(batched) != len(singles) {
		t.Fatalf("batch len = %d, want %d", len(batched), len(singles))
	}

	for i := range singles {
		if batched[i].Type != singles[i].Type {
			t.Errorf("[%d] batch Type=%q, single Type=%q", i, batched[i].Type, singles[i].Type)
		}
		if batched[i].Category != singles[i].Category {
			t.Errorf("[%d] batch Category=%q, single Category=%q", i, batched[i].Category, singles[i].Category)
		}
		// Confidence may differ slightly due to dynamic padding differences.
		// Allow a small tolerance.
		diff := batched[i].Confidence - singles[i].Confidence
		if diff < -0.05 || diff > 0.05 {
			t.Errorf("[%d] confidence diverged: batch=%.4f, single=%.4f", i, batched[i].Confidence, singles[i].Confidence)
		}
	}
}

func TestProcessEmptyBatch(t *testing.T) {
	eng := newTestEngine(t)

	events, err := eng.ProcessBatch(nil)
	if err != nil {
		t.Fatalf("ProcessBatch(nil) error: %v", err)
	}
	if events != nil {
		t.Errorf("expected nil, got %d events", len(events))
	}
}

func TestProcessUnclassifiedLog(t *testing.T) {
	eng := newTestEngine(t)

	raw := model.RawLog{
		Raw:       "xkcd 927 lorem ipsum dolor sit amet 42 foo bar baz",
		Timestamp: time.Now(),
	}

	event, err := eng.Process(raw)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	t.Logf("Gibberish classified as: %s.%s (confidence=%.3f)", event.Type, event.Category, event.Confidence)
	// We log regardless, but if it classifies as UNCLASSIFIED, verify the structure.
	if event.Type == "UNCLASSIFIED" {
		if event.Category != "" {
			t.Errorf("UNCLASSIFIED event should have empty category, got %q", event.Category)
		}
	}
}

func TestCorpusAccuracy(t *testing.T) {
	eng := newTestEngine(t)

	corpus, err := testdata.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus() error: %v", err)
	}

	correct := 0
	incorrect := 0
	unclassified := 0

	// Track per-category stats.
	type catStat struct {
		correct   int
		incorrect int
		total     int
	}
	catStats := map[string]*catStat{}

	// Track misclassifications for debugging.
	type misclass struct {
		raw        string
		expected   string
		got        string
		confidence float64
	}
	var misses []misclass

	for _, entry := range corpus {
		raw := model.RawLog{Raw: entry.Raw, Timestamp: time.Now()}
		event, err := eng.Process(raw)
		if err != nil {
			t.Fatalf("Process() error on %q: %v", entry.Description, err)
		}

		expectedPath := entry.ExpectedType + "." + entry.ExpectedCategory
		gotPath := event.Type + "." + event.Category
		if event.Type == "UNCLASSIFIED" {
			gotPath = "UNCLASSIFIED"
		}

		stat, ok := catStats[expectedPath]
		if !ok {
			stat = &catStat{}
			catStats[expectedPath] = stat
		}
		stat.total++

		if gotPath == expectedPath {
			correct++
			stat.correct++
		} else if event.Type == "UNCLASSIFIED" {
			unclassified++
			stat.incorrect++
			misses = append(misses, misclass{
				raw:        entry.Description,
				expected:   expectedPath,
				got:        "UNCLASSIFIED",
				confidence: event.Confidence,
			})
		} else {
			incorrect++
			stat.incorrect++
			misses = append(misses, misclass{
				raw:        entry.Description,
				expected:   expectedPath,
				got:        gotPath,
				confidence: event.Confidence,
			})
		}
	}

	total := len(corpus)
	accuracy := float64(correct) / float64(total) * 100

	t.Logf("\n=== Corpus Accuracy Report ===")
	t.Logf("Total: %d | Correct: %d | Incorrect: %d | Unclassified: %d", total, correct, incorrect, unclassified)
	t.Logf("Accuracy: %.1f%%\n", accuracy)

	// Print misclassifications.
	if len(misses) > 0 {
		t.Logf("--- Misclassifications ---")
		for _, m := range misses {
			t.Logf("  %-40s expected=%-35s got=%-35s conf=%.3f", m.raw, m.expected, m.got, m.confidence)
		}
	}

	// Print per-category stats for categories with errors.
	t.Logf("\n--- Per-Category Accuracy ---")
	for path, stat := range catStats {
		pct := float64(stat.correct) / float64(stat.total) * 100
		marker := ""
		if stat.incorrect > 0 {
			marker = " <<<"
		}
		t.Logf("  %-35s %d/%d (%.0f%%)%s", path, stat.correct, stat.total, pct, marker)
	}

	if accuracy < 80 {
		t.Errorf("Classification accuracy %.1f%% is below 80%% threshold", accuracy)
	}
}

func TestCorpusSeverityConsistency(t *testing.T) {
	eng := newTestEngine(t)

	corpus, err := testdata.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus() error: %v", err)
	}

	mismatches := 0
	for _, entry := range corpus {
		raw := model.RawLog{Raw: entry.Raw, Timestamp: time.Now()}
		event, err := eng.Process(raw)
		if err != nil {
			t.Fatalf("Process() error on %q: %v", entry.Description, err)
		}

		// Only check severity for correctly classified entries.
		gotPath := event.Type + "." + event.Category
		expectedPath := entry.ExpectedType + "." + entry.ExpectedCategory
		if gotPath != expectedPath {
			continue
		}

		if event.Severity != entry.ExpectedSeverity {
			t.Errorf("%-40s severity=%q, want %q (classified as %s)",
				entry.Description, event.Severity, entry.ExpectedSeverity, gotPath)
			mismatches++
		}
	}

	if mismatches > 0 {
		t.Logf("%d severity mismatches in correctly classified entries", mismatches)
	} else {
		t.Log("All correctly classified entries have correct severity")
	}
}

func TestCorpusConfidenceDistribution(t *testing.T) {
	eng := newTestEngine(t)

	corpus, err := testdata.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus() error: %v", err)
	}

	// Classify with threshold=0 to see all raw confidence scores.
	// We use the engine as-is since it already classified everything
	// (threshold only affects the UNCLASSIFIED label, not the score).
	type scored struct {
		confidence float64
		correct    bool
	}
	var results []scored

	var correctConfs, incorrectConfs []float64

	for _, entry := range corpus {
		raw := model.RawLog{Raw: entry.Raw, Timestamp: time.Now()}
		event, err := eng.Process(raw)
		if err != nil {
			t.Fatalf("Process() error: %v", err)
		}

		gotPath := fmt.Sprintf("%s.%s", event.Type, event.Category)
		expectedPath := fmt.Sprintf("%s.%s", entry.ExpectedType, entry.ExpectedCategory)
		if event.Type == "UNCLASSIFIED" {
			gotPath = "UNCLASSIFIED"
		}

		isCorrect := gotPath == expectedPath
		results = append(results, scored{confidence: event.Confidence, correct: isCorrect})

		if isCorrect {
			correctConfs = append(correctConfs, event.Confidence)
		} else {
			incorrectConfs = append(incorrectConfs, event.Confidence)
		}
	}

	t.Logf("\n=== Confidence Distribution ===")
	if len(correctConfs) > 0 {
		mean := avg(correctConfs)
		mn, mx := minMax(correctConfs)
		t.Logf("Correct   (n=%d): mean=%.3f, min=%.3f, max=%.3f", len(correctConfs), mean, mn, mx)
	}
	if len(incorrectConfs) > 0 {
		mean := avg(incorrectConfs)
		mn, mx := minMax(incorrectConfs)
		t.Logf("Incorrect (n=%d): mean=%.3f, min=%.3f, max=%.3f", len(incorrectConfs), mean, mn, mx)
	}

	// Threshold sweep: test thresholds from 0.50 to 0.85 and report
	// how many correct would be kept vs incorrect rejected at each level.
	t.Logf("\n--- Threshold Sweep ---")
	t.Logf("  %-12s %10s %10s %10s", "Threshold", "Correct↑", "Incorrect↑", "Net Accuracy")
	for _, th := range []float64{0.50, 0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85} {
		correctAbove := 0
		incorrectAbove := 0
		for _, r := range results {
			if r.confidence >= th {
				if r.correct {
					correctAbove++
				} else {
					incorrectAbove++
				}
			}
		}
		classified := correctAbove + incorrectAbove
		netAcc := 0.0
		if classified > 0 {
			netAcc = float64(correctAbove) / float64(classified) * 100
		}
		t.Logf("  %-12.2f %10d %10d %13.1f%%", th, correctAbove, incorrectAbove, netAcc)
	}

	// Suggestion based on data.
	if len(correctConfs) > 0 && len(incorrectConfs) > 0 {
		correctMin, _ := minMax(correctConfs)
		incorrectMean := avg(incorrectConfs)
		gap := correctMin - incorrectMean
		t.Logf("\nCorrect min (%.3f) - Incorrect mean (%.3f) = gap %.3f", correctMin, incorrectMean, gap)
		if gap < 0.05 {
			t.Logf("Distributions overlap significantly — threshold alone cannot separate correct from incorrect.")
			t.Logf("Primary lever is taxonomy description tuning (Section 5).")
		} else {
			suggested := (correctMin + incorrectMean) / 2
			t.Logf("Suggested threshold: %.2f (midpoint of gap)", suggested)
		}
	}
}

// --- Section 6: Edge Cases & Robustness ---

// panicEmbedder is a mock that panics if called — used to verify early returns
// bypass the embedding path. Runs without ONNX model files.
type panicEmbedder struct{}

func (p panicEmbedder) Embed(string) ([]float32, error)        { panic("Embed called on empty input") }
func (p panicEmbedder) EmbedBatch([]string) ([][]float32, error) { panic("EmbedBatch called on empty input") }
func (p panicEmbedder) Close() error                            { return nil }

func TestProcessEmptyLog_ReturnsUnclassified(t *testing.T) {
	// Uses panicEmbedder — no ONNX required. Proves the early return works.
	eng := New(panicEmbedder{}, nil, nil, nil)

	ts := time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC)
	event, err := eng.Process(model.RawLog{Raw: "", Timestamp: ts})
	if err != nil {
		t.Fatalf("Process(empty) error: %v", err)
	}
	if event.Type != "UNCLASSIFIED" {
		t.Errorf("Type = %q, want UNCLASSIFIED", event.Type)
	}
	if event.Category != "empty_input" {
		t.Errorf("Category = %q, want empty_input", event.Category)
	}
	if event.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", event.Severity)
	}
	if event.Confidence != 0 {
		t.Errorf("Confidence = %f, want 0", event.Confidence)
	}
	if !event.Timestamp.Equal(ts) {
		t.Errorf("Timestamp not preserved: got %v, want %v", event.Timestamp, ts)
	}
}

func TestProcessWhitespaceLog_ReturnsUnclassified(t *testing.T) {
	eng := New(panicEmbedder{}, nil, nil, nil)

	event, err := eng.Process(model.RawLog{Raw: "   \n\t  ", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Process(whitespace) error: %v", err)
	}
	if event.Type != "UNCLASSIFIED" {
		t.Errorf("Type = %q, want UNCLASSIFIED", event.Type)
	}
	if event.Category != "empty_input" {
		t.Errorf("Category = %q, want empty_input", event.Category)
	}
	if event.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", event.Severity)
	}
}

func TestProcessEmptyLog(t *testing.T) {
	eng := newTestEngine(t)

	event, err := eng.Process(model.RawLog{Raw: "", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Process(empty) error: %v", err)
	}
	if event.Type != "UNCLASSIFIED" {
		t.Errorf("Type = %q, want UNCLASSIFIED", event.Type)
	}
	if event.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", event.Severity)
	}
}

func TestProcessWhitespaceLog(t *testing.T) {
	eng := newTestEngine(t)

	event, err := eng.Process(model.RawLog{Raw: "   \n\t  ", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Process(whitespace) error: %v", err)
	}
	if event.Type != "UNCLASSIFIED" {
		t.Errorf("Type = %q, want UNCLASSIFIED", event.Type)
	}
	if event.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", event.Severity)
	}
}

func TestProcessBatchAllEmpty_SkipsEmbedder(t *testing.T) {
	// Uses panicEmbedder — proves EmbedBatch is never called when all inputs are empty.
	eng := New(panicEmbedder{}, nil, nil, nil)

	ts := time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC)
	events, err := eng.ProcessBatch([]model.RawLog{
		{Raw: "", Timestamp: ts},
		{Raw: "   \n\t  ", Timestamp: ts},
	})
	if err != nil {
		t.Fatalf("ProcessBatch(all empty) error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for i, e := range events {
		if e.Type != "UNCLASSIFIED" {
			t.Errorf("events[%d].Type = %q, want UNCLASSIFIED", i, e.Type)
		}
		if e.Category != "empty_input" {
			t.Errorf("events[%d].Category = %q, want empty_input", i, e.Category)
		}
	}
}

func TestProcessVeryLongLog(t *testing.T) {
	eng := newTestEngine(t)

	// Build a log line that far exceeds 128 tokens. The signal is at the start.
	long := "ERROR connection refused to database host=db-primary port=5432 " + strings.Repeat("extra padding data filler text here ", 100)
	event, err := eng.Process(model.RawLog{Raw: long, Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Process(long) error: %v", err)
	}

	t.Logf("Long log (%d chars): type=%q category=%q confidence=%.3f", len(long), event.Type, event.Category, event.Confidence)

	// Should still classify reasonably — the first 128 tokens contain the signal.
	if event.Type != "ERROR" {
		t.Errorf("expected Type=ERROR for long log with error signal at start, got %q", event.Type)
	}
}

func TestProcessBinaryContent(t *testing.T) {
	eng := newTestEngine(t)

	// Binary data with null bytes and invalid UTF-8.
	binary := "ERROR \x00\x01\x02\xff\xfe some binary \x80\x81 data \x00 in log"
	event, err := eng.Process(model.RawLog{Raw: binary, Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("Process(binary) error: %v", err)
	}
	t.Logf("Binary log: type=%q category=%q confidence=%.3f", event.Type, event.Category, event.Confidence)
	// Must not crash. Classification quality is not important for binary input.
}

func TestProcessTimestampPreservation(t *testing.T) {
	eng := newTestEngine(t)

	ts := time.Date(2026, 2, 19, 12, 34, 56, 789000000, time.UTC)
	event, err := eng.Process(model.RawLog{
		Raw:       "INFO test log",
		Timestamp: ts,
	})
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if !event.Timestamp.Equal(ts) {
		t.Errorf("Timestamp not preserved: got %v, want %v", event.Timestamp, ts)
	}
}

func TestProcessZeroTimestamp(t *testing.T) {
	eng := newTestEngine(t)

	event, err := eng.Process(model.RawLog{Raw: "INFO test log"})
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if !event.Timestamp.IsZero() {
		t.Errorf("Zero timestamp not preserved: got %v", event.Timestamp)
	}
}

func TestProcessMetadataNotInOutput(t *testing.T) {
	eng := newTestEngine(t)

	raw := model.RawLog{
		Raw:       "ERROR connection refused",
		Timestamp: time.Now(),
		Source:    "vercel",
		Metadata:  map[string]any{"project_id": "prj_123", "deployment_id": "dpl_456"},
	}

	event, err := eng.Process(raw)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	// CanonicalEvent currently has no metadata field — verify the pipeline
	// doesn't crash when metadata is present on the input.
	if event.Type == "" {
		t.Error("Type is empty")
	}
	t.Log("Metadata passthrough: not surfaced in CanonicalEvent (by design for Phase 2)")
}

// --- Corpus validation wrappers ---
// These ensure corpus structural issues are caught by `go test ./...`,
// which skips the testdata/ package (Go convention).

func TestCorpusStructure(t *testing.T) {
	corpus, err := testdata.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus() error: %v", err)
	}

	if len(corpus) == 0 {
		t.Fatal("corpus is empty")
	}

	for i, e := range corpus {
		if e.Raw == "" {
			t.Errorf("entry[%d] has empty raw", i)
		}
		if e.ExpectedType == "" {
			t.Errorf("entry[%d] has empty expected_type", i)
		}
		if e.ExpectedCategory == "" {
			t.Errorf("entry[%d] has empty expected_category", i)
		}
		if e.ExpectedSeverity == "" {
			t.Errorf("entry[%d] has empty expected_severity", i)
		}
	}

	t.Logf("Corpus: %d entries, all fields populated", len(corpus))
}

func TestCorpusTaxonomyCoverage(t *testing.T) {
	corpus, err := testdata.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus() error: %v", err)
	}

	covered := map[string]int{}
	for _, e := range corpus {
		covered[e.ExpectedType+"."+e.ExpectedCategory]++
	}

	// All 42 taxonomy leaves must be covered with at least 2 entries.
	allLeaves := []string{
		"ERROR.connection_failure", "ERROR.auth_failure", "ERROR.authorization_failure",
		"ERROR.timeout", "ERROR.runtime_exception", "ERROR.validation_error",
		"ERROR.out_of_memory", "ERROR.rate_limited", "ERROR.dependency_error",
		"REQUEST.success", "REQUEST.client_error", "REQUEST.server_error",
		"REQUEST.redirect", "REQUEST.slow_request",
		"DEPLOY.build_started", "DEPLOY.build_succeeded", "DEPLOY.build_failed",
		"DEPLOY.deploy_started", "DEPLOY.deploy_succeeded", "DEPLOY.deploy_failed",
		"DEPLOY.rollback",
		"SYSTEM.health_check", "SYSTEM.scaling_event", "SYSTEM.resource_alert",
		"SYSTEM.process_lifecycle", "SYSTEM.config_change",
		"ACCESS.login_success", "ACCESS.login_failure", "ACCESS.session_expired",
		"ACCESS.permission_change", "ACCESS.api_key_event",
		"PERFORMANCE.latency_spike", "PERFORMANCE.throughput_drop", "PERFORMANCE.queue_backlog",
		"PERFORMANCE.cache_event", "PERFORMANCE.db_slow_query",
		"DATA.query_executed", "DATA.migration", "DATA.replication",
		"SCHEDULED.cron_started", "SCHEDULED.cron_completed", "SCHEDULED.cron_failed",
	}

	for _, leaf := range allLeaves {
		count := covered[leaf]
		if count == 0 {
			t.Errorf("taxonomy leaf %q has no corpus entries", leaf)
		} else if count < 2 {
			t.Errorf("taxonomy leaf %q has only %d entry (want >= 2)", leaf, count)
		}
	}

	t.Logf("Coverage: %d leaves, %d total entries", len(allLeaves), len(corpus))
}

func avg(vs []float64) float64 {
	sum := 0.0
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

func minMax(vs []float64) (float64, float64) {
	mn, mx := vs[0], vs[0]
	for _, v := range vs[1:] {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}
