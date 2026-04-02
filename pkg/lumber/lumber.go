package lumber

import (
	"fmt"
	"os"
	"time"

	"github.com/kaminocorp/lumber/internal/engine"
	"github.com/kaminocorp/lumber/internal/engine/classifier"
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/engine/taxonomy"
	"github.com/kaminocorp/lumber/internal/model"
)

// Lumber is a log classification engine.
// It embeds log text into vectors and classifies against a 42-label taxonomy.
// Safe for concurrent use.
type Lumber struct {
	engine   *engine.Engine
	embedder embedder.Embedder
	taxonomy *taxonomy.Taxonomy
}

// New creates a Lumber instance, loading model files and pre-embedding
// the taxonomy. This is an expensive operation (~100-300ms) — create once,
// reuse across requests.
func New(opts ...Option) (*Lumber, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	// Auto-download models + ORT if requested and no explicit paths provided.
	if o.autoDownload && o.modelDir == "" && o.modelPath == "" {
		cacheDir := o.cacheDir
		if cacheDir == "" {
			var err error
			cacheDir, err = defaultCacheDir()
			if err != nil {
				return nil, fmt.Errorf("lumber: %w", err)
			}
		}
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return nil, fmt.Errorf("lumber: creating cache dir: %w", err)
		}
		if err := downloadModels(cacheDir); err != nil {
			return nil, fmt.Errorf("lumber: %w", err)
		}
		if err := downloadORT(cacheDir); err != nil {
			return nil, fmt.Errorf("lumber: %w", err)
		}
		o.modelDir = cacheDir
	}

	modelPath, vocabPath, projPath := resolvePaths(o)

	emb, err := embedder.New(modelPath, vocabPath, projPath)
	if err != nil {
		return nil, fmt.Errorf("lumber: %w", err)
	}

	tax, err := taxonomy.New(taxonomy.DefaultRoots(), emb)
	if err != nil {
		emb.Close()
		return nil, fmt.Errorf("lumber: %w", err)
	}

	cls := classifier.New(o.confidenceThreshold)
	cmp := compactor.New(parseVerbosity(o.verbosity))
	eng := engine.New(emb, tax, cls, cmp)

	return &Lumber{engine: eng, embedder: emb, taxonomy: tax}, nil
}

// Classify classifies a single log line and returns a canonical event.
func (l *Lumber) Classify(text string) (Event, error) {
	raw := model.RawLog{
		Timestamp: time.Now(),
		Raw:       text,
	}
	ce, err := l.engine.Process(raw)
	if err != nil {
		return Event{}, err
	}
	return eventFromCanonical(ce), nil
}

// ClassifyBatch classifies multiple log lines in a single batched inference call.
// More efficient than calling Classify in a loop.
func (l *Lumber) ClassifyBatch(texts []string) ([]Event, error) {
	raws := make([]model.RawLog, len(texts))
	now := time.Now()
	for i, t := range texts {
		raws[i] = model.RawLog{Timestamp: now, Raw: t}
	}
	ces, err := l.engine.ProcessBatch(raws)
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(ces))
	for i, ce := range ces {
		events[i] = eventFromCanonical(ce)
	}
	return events, nil
}

// ClassifyLog classifies a structured log entry. Use this when you have
// timestamp and source information. For raw text, use Classify().
func (l *Lumber) ClassifyLog(log Log) (Event, error) {
	ts := log.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	raw := model.RawLog{
		Timestamp: ts,
		Source:    log.Source,
		Raw:       log.Text,
		Metadata:  log.Metadata,
	}
	ce, err := l.engine.Process(raw)
	if err != nil {
		return Event{}, err
	}
	return eventFromCanonical(ce), nil
}

// ClassifyLogs classifies a batch of structured log entries.
func (l *Lumber) ClassifyLogs(logs []Log) ([]Event, error) {
	raws := make([]model.RawLog, len(logs))
	now := time.Now()
	for i, log := range logs {
		ts := log.Timestamp
		if ts.IsZero() {
			ts = now
		}
		raws[i] = model.RawLog{
			Timestamp: ts,
			Source:    log.Source,
			Raw:       log.Text,
			Metadata:  log.Metadata,
		}
	}
	ces, err := l.engine.ProcessBatch(raws)
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(ces))
	for i, ce := range ces {
		events[i] = eventFromCanonical(ce)
	}
	return events, nil
}

// Close releases model resources (ONNX runtime, memory).
// Must be called when the Lumber instance is no longer needed.
func (l *Lumber) Close() error {
	return l.embedder.Close()
}

// eventFromCanonical converts the internal CanonicalEvent to the public Event type.
func eventFromCanonical(ce model.CanonicalEvent) Event {
	return Event{
		Type:       ce.Type,
		Category:   ce.Category,
		Severity:   ce.Severity,
		Timestamp:  ce.Timestamp,
		Summary:    ce.Summary,
		Confidence: ce.Confidence,
		Raw:        ce.Raw,
		Count:      ce.Count,
	}
}

// parseVerbosity maps a string to the internal Verbosity enum.
func parseVerbosity(s string) compactor.Verbosity {
	switch s {
	case "minimal":
		return compactor.Minimal
	case "full":
		return compactor.Full
	default:
		return compactor.Standard
	}
}
