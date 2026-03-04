package taxonomy

import (
	"fmt"

	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/model"
)

// Taxonomy manages the label tree and pre-embedded label vectors.
type Taxonomy struct {
	root   []*model.TaxonomyNode
	labels []model.EmbeddedLabel
}

// New creates a Taxonomy from a set of root nodes and pre-embeds all leaf labels.
// Each leaf is embedded using the text "{Parent}: {Leaf.Desc}" to capture both
// the category context and the semantic description.
func New(roots []*model.TaxonomyNode, emb embedder.Embedder) (*Taxonomy, error) {
	// Collect leaf paths, severities, and embedding texts.
	var paths []string
	var severities []string
	var texts []string
	for _, root := range roots {
		for _, child := range root.Children {
			paths = append(paths, root.Name+"."+child.Name)
			severities = append(severities, child.Severity)
			texts = append(texts, root.Name+": "+child.Desc)
		}
	}

	if len(texts) == 0 {
		return &Taxonomy{root: roots}, nil
	}

	vecs, err := emb.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("taxonomy: pre-embed %d labels: %w", len(texts), err)
	}

	labels := make([]model.EmbeddedLabel, len(paths))
	for i := range paths {
		labels[i] = model.EmbeddedLabel{Path: paths[i], Vector: vecs[i], Severity: severities[i]}
	}

	return &Taxonomy{root: roots, labels: labels}, nil
}

// Labels returns the pre-embedded taxonomy labels for classification.
func (t *Taxonomy) Labels() []model.EmbeddedLabel {
	return t.labels
}

// Roots returns the top-level taxonomy nodes.
func (t *Taxonomy) Roots() []*model.TaxonomyNode {
	return t.root
}
