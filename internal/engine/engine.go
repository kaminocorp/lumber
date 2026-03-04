package engine

import (
	"strings"

	"github.com/kaminocorp/lumber/internal/engine/classifier"
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/engine/taxonomy"
	"github.com/kaminocorp/lumber/internal/model"
)

// Engine orchestrates the embed → classify → compact pipeline.
type Engine struct {
	embedder   embedder.Embedder
	taxonomy   *taxonomy.Taxonomy
	classifier *classifier.Classifier
	compactor  *compactor.Compactor
}

// New creates an Engine with the provided components.
func New(emb embedder.Embedder, tax *taxonomy.Taxonomy, cls *classifier.Classifier, cmp *compactor.Compactor) *Engine {
	return &Engine{
		embedder:   emb,
		taxonomy:   tax,
		classifier: cls,
		compactor:  cmp,
	}
}

// Process classifies and compacts a single raw log into a canonical event.
func (e *Engine) Process(raw model.RawLog) (model.CanonicalEvent, error) {
	// Empty/whitespace input cannot be meaningfully classified.
	if strings.TrimSpace(raw.Raw) == "" {
		return emptyInputEvent(raw), nil
	}

	vec, err := e.embedder.Embed(raw.Raw)
	if err != nil {
		return model.CanonicalEvent{}, err
	}

	result := e.classifier.Classify(vec, e.taxonomy.Labels())

	parts := strings.SplitN(result.Label.Path, ".", 2)
	eventType := parts[0]
	category := ""
	if len(parts) > 1 {
		category = parts[1]
	}

	compacted, summary := e.compactor.Compact(raw.Raw, eventType)

	severity := result.Label.Severity
	if eventType == "UNCLASSIFIED" && severity == "" {
		severity = "warning"
	}

	return model.CanonicalEvent{
		Type:       eventType,
		Category:   category,
		Severity:   severity,
		Timestamp:  raw.Timestamp,
		Summary:    summary,
		Confidence: result.Confidence,
		Raw:        compacted,
	}, nil
}

// ProcessBatch classifies and compacts a slice of raw logs using a single
// batched ONNX inference call. Empty/whitespace inputs are handled without
// invoking the embedder.
func (e *Engine) ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error) {
	if len(raws) == 0 {
		return nil, nil
	}

	events := make([]model.CanonicalEvent, len(raws))

	// Separate non-empty inputs for batched embedding. Track their indices
	// so we can map vectors back to the original positions.
	var embedTexts []string
	var embedIndices []int
	for i, raw := range raws {
		if strings.TrimSpace(raw.Raw) == "" {
			events[i] = emptyInputEvent(raw)
		} else {
			embedTexts = append(embedTexts, raw.Raw)
			embedIndices = append(embedIndices, i)
		}
	}

	// If all inputs were empty, we're done.
	if len(embedTexts) == 0 {
		return events, nil
	}

	vecs, err := e.embedder.EmbedBatch(embedTexts)
	if err != nil {
		return nil, err
	}

	for vi, origIdx := range embedIndices {
		raw := raws[origIdx]
		result := e.classifier.Classify(vecs[vi], e.taxonomy.Labels())

		parts := strings.SplitN(result.Label.Path, ".", 2)
		eventType := parts[0]
		category := ""
		if len(parts) > 1 {
			category = parts[1]
		}

		compacted, summary := e.compactor.Compact(raw.Raw, eventType)

		severity := result.Label.Severity
		if eventType == "UNCLASSIFIED" && severity == "" {
			severity = "warning"
		}

		events[origIdx] = model.CanonicalEvent{
			Type:       eventType,
			Category:   category,
			Severity:   severity,
			Timestamp:  raw.Timestamp,
			Summary:    summary,
			Confidence: result.Confidence,
			Raw:        compacted,
		}
	}
	return events, nil
}

// emptyInputEvent returns an UNCLASSIFIED event for empty/whitespace-only input.
func emptyInputEvent(raw model.RawLog) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:       "UNCLASSIFIED",
		Category:   "empty_input",
		Severity:   "warning",
		Timestamp:  raw.Timestamp,
		Confidence: 0,
		Raw:        raw.Raw,
	}
}
