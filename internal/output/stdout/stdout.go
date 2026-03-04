package stdout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

// Output writes JSON-encoded canonical events to stdout.
type Output struct {
	enc       *json.Encoder
	verbosity compactor.Verbosity
}

// New creates a new stdout Output with verbosity-aware field omission
// and optional pretty-printed JSON.
func New(verbosity compactor.Verbosity, pretty bool) *Output {
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return &Output{enc: enc, verbosity: verbosity}
}

func (o *Output) Write(_ context.Context, event model.CanonicalEvent) error {
	formatted := output.FormatEvent(event, o.verbosity)
	if err := o.enc.Encode(formatted); err != nil {
		return fmt.Errorf("stdout output: %w", err)
	}
	return nil
}

func (o *Output) Close() error {
	return nil
}
