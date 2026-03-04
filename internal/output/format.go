package output

import (
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
)

// FormatEvent returns a copy of the event with fields stripped according to verbosity.
// At Minimal: Raw and Confidence are zeroed (omitted from JSON via omitempty).
// At Standard/Full: all fields preserved.
func FormatEvent(e model.CanonicalEvent, verbosity compactor.Verbosity) model.CanonicalEvent {
	if verbosity == compactor.Minimal {
		e.Raw = ""
		e.Confidence = 0
	}
	return e
}
