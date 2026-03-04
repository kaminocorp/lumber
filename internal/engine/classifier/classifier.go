package classifier

import (
	"math"

	"github.com/kaminocorp/lumber/internal/model"
)

// Result holds the outcome of classifying a single log embedding.
type Result struct {
	Label      model.EmbeddedLabel
	Confidence float64
}

// Classifier scores a log embedding against pre-embedded taxonomy labels.
type Classifier struct {
	Threshold float64
}

// New creates a Classifier with the given confidence threshold.
func New(threshold float64) *Classifier {
	return &Classifier{Threshold: threshold}
}

// Classify finds the best-matching taxonomy label for the given embedding vector.
// Returns the top match. If confidence is below threshold, Label.Path will be "UNCLASSIFIED".
func (c *Classifier) Classify(vector []float32, labels []model.EmbeddedLabel) Result {
	if len(labels) == 0 {
		return Result{Label: model.EmbeddedLabel{Path: "UNCLASSIFIED"}, Confidence: 0}
	}

	best := Result{Confidence: -1}
	for _, lbl := range labels {
		sim := cosineSimilarity(vector, lbl.Vector)
		if sim > best.Confidence {
			best = Result{Label: lbl, Confidence: sim}
		}
	}

	if best.Confidence < c.Threshold {
		return Result{Label: model.EmbeddedLabel{Path: "UNCLASSIFIED"}, Confidence: best.Confidence}
	}
	return best
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
