package embedder

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ortLibraryName returns the platform-specific ONNX Runtime shared library filename.
func ortLibraryName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "libonnxruntime.so"
	}
}

// ortEnv manages global ONNX Runtime initialization (process-wide singleton).
var ortEnv struct {
	once sync.Once
	err  error
}

// initORT initializes the ONNX Runtime environment. Safe to call multiple
// times; only the first call has any effect.
func initORT(libPath string) error {
	ortEnv.once.Do(func() {
		ort.SetSharedLibraryPath(libPath)
		ortEnv.err = ort.InitializeEnvironment()
	})
	return ortEnv.err
}

// onnxSession wraps a DynamicAdvancedSession for BERT-style models.
type onnxSession struct {
	session    *ort.DynamicAdvancedSession
	inputNames []string
	outputName string
	embedDim   int64
}

// newONNXSession loads the ONNX model and creates an inference session.
// It validates the model's input/output tensor names and shapes.
func newONNXSession(modelPath string) (*onnxSession, error) {
	// Resolve the ONNX Runtime shared library path. We ship it alongside the
	// model files in the models/ directory.
	modelDir := filepath.Dir(modelPath)
	libPath := filepath.Join(modelDir, ortLibraryName())

	if err := initORT(libPath); err != nil {
		return nil, fmt.Errorf("onnx: failed to initialize runtime: %w", err)
	}

	// Inspect model to discover tensor names and shapes.
	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to read model info: %w", err)
	}

	// Validate expected BERT-style inputs.
	inputNames, err := validateInputs(inputs)
	if err != nil {
		return nil, err
	}

	// Validate output — expect a single tensor with shape [batch, seq, dim].
	if len(outputs) == 0 {
		return nil, fmt.Errorf("onnx: model has no outputs")
	}
	outputName := outputs[0].Name
	dims := outputs[0].Dimensions
	if len(dims) != 3 {
		return nil, fmt.Errorf("onnx: expected 3D output tensor, got %v", dims)
	}
	embedDim := dims[2]

	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create session options: %w", err)
	}
	defer opts.Destroy()
	opts.SetIntraOpNumThreads(4)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		inputNames,
		[]string{outputName},
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create session: %w", err)
	}

	return &onnxSession{
		session:    session,
		inputNames: inputNames,
		outputName: outputName,
		embedDim:   embedDim,
	}, nil
}

// validateInputs checks that the model has the expected BERT-style inputs
// and returns them in the correct order.
func validateInputs(inputs []ort.InputOutputInfo) ([]string, error) {
	nameSet := make(map[string]bool, len(inputs))
	for _, inp := range inputs {
		nameSet[inp.Name] = true
	}
	required := []string{"input_ids", "attention_mask", "token_type_ids"}
	for _, name := range required {
		if !nameSet[name] {
			return nil, fmt.Errorf("onnx: model missing required input %q", name)
		}
	}
	return required, nil
}

// infer runs a single inference call. inputIDs, attentionMask, and
// tokenTypeIDs are flat [batchSize * seqLen] slices. Returns the raw output
// tensor data as a flat float32 slice of shape [batchSize * seqLen * embedDim].
func (s *onnxSession) infer(inputIDs, attentionMask, tokenTypeIDs []int64, batchSize, seqLen int64) ([]float32, error) {
	shape := ort.NewShape(batchSize, seqLen)

	tIDs, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create input_ids tensor: %w", err)
	}
	defer tIDs.Destroy()

	tMask, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create attention_mask tensor: %w", err)
	}
	defer tMask.Destroy()

	tTypes, err := ort.NewTensor(shape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create token_type_ids tensor: %w", err)
	}
	defer tTypes.Destroy()

	outShape := ort.NewShape(batchSize, seqLen, s.embedDim)
	tOut, err := ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create output tensor: %w", err)
	}
	defer tOut.Destroy()

	err = s.session.Run(
		[]ort.Value{tIDs, tMask, tTypes},
		[]ort.Value{tOut},
	)
	if err != nil {
		return nil, fmt.Errorf("onnx: inference failed: %w", err)
	}

	// Copy data out before tensor is destroyed.
	src := tOut.GetData()
	result := make([]float32, len(src))
	copy(result, src)
	return result, nil
}

// close releases the ONNX session resources.
func (s *onnxSession) close() error {
	return s.session.Destroy()
}
