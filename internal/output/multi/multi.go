package multi

import (
	"context"
	"errors"

	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

// Multi fans out events to multiple output.Output implementations.
// Each Write call delivers the event to every wrapped output sequentially.
// If one output fails, the remaining outputs still receive the event.
type Multi struct {
	outputs []output.Output
}

// New creates a Multi that fans out to the given outputs.
func New(outputs ...output.Output) *Multi {
	return &Multi{outputs: outputs}
}

// Write delivers the event to every wrapped output. Errors are collected
// but do not prevent delivery to subsequent outputs.
func (m *Multi) Write(ctx context.Context, event model.CanonicalEvent) error {
	var errs []error
	for _, o := range m.outputs {
		if err := o.Write(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close calls Close on every wrapped output, collecting errors.
func (m *Multi) Close() error {
	var errs []error
	for _, o := range m.outputs {
		if err := o.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
