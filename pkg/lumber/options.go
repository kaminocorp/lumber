package lumber

import "path/filepath"

type options struct {
	modelDir            string
	modelPath           string
	vocabPath           string
	projectionPath      string
	confidenceThreshold float64
	verbosity           string
	autoDownload        bool
	cacheDir            string
}

// Option configures a Lumber instance.
type Option func(*options)

// WithModelDir sets the directory containing model files.
// Expects: model_quantized.onnx, vocab.txt, 2_Dense/model.safetensors.
func WithModelDir(dir string) Option {
	return func(o *options) {
		o.modelDir = dir
	}
}

// WithModelPaths sets explicit paths for each model file.
// Use this when model files aren't in the default directory layout.
func WithModelPaths(model, vocab, projection string) Option {
	return func(o *options) {
		o.modelPath = model
		o.vocabPath = vocab
		o.projectionPath = projection
	}
}

// WithConfidenceThreshold sets the minimum cosine similarity for classification.
// Below this threshold, events are marked UNCLASSIFIED. Default: 0.5.
func WithConfidenceThreshold(t float64) Option {
	return func(o *options) {
		o.confidenceThreshold = t
	}
}

// WithVerbosity sets the compaction verbosity: "minimal", "standard", "full".
// Default: "standard".
func WithVerbosity(v string) Option {
	return func(o *options) {
		o.verbosity = v
	}
}

// WithAutoDownload enables automatic model and ONNX Runtime download.
// On first call, downloads ~35-60MB of files to the OS cache directory
// (or the directory specified by WithCacheDir). Subsequent calls reuse
// the cached files. Requires network access on first run only.
//
// When combined with WithModelDir or WithModelPaths, those take precedence
// and no download is attempted.
func WithAutoDownload() Option {
	return func(o *options) {
		o.autoDownload = true
	}
}

// WithCacheDir overrides the default cache directory for auto-downloaded files.
// Only relevant when WithAutoDownload is used. Default: ~/.cache/lumber (Linux),
// ~/Library/Caches/lumber (macOS).
func WithCacheDir(dir string) Option {
	return func(o *options) {
		o.cacheDir = dir
	}
}

func defaultOptions() options {
	return options{
		confidenceThreshold: 0.5,
		verbosity:           "standard",
	}
}

// resolvePaths determines the model, vocab, and projection file paths
// from the configured options. Explicit paths take precedence over modelDir.
func resolvePaths(o options) (model, vocab, projection string) {
	if o.modelPath != "" {
		return o.modelPath, o.vocabPath, o.projectionPath
	}
	dir := o.modelDir
	if dir == "" {
		dir = "models"
	}
	return filepath.Join(dir, "model_quantized.onnx"),
		filepath.Join(dir, "vocab.txt"),
		filepath.Join(dir, "2_Dense", "model.safetensors")
}
