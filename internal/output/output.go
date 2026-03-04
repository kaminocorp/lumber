package output

import (
	"context"

	"github.com/kaminocorp/lumber/internal/model"
)

// Output defines the interface for canonical event destinations.
type Output interface {
	Write(ctx context.Context, event model.CanonicalEvent) error
	Close() error
}
