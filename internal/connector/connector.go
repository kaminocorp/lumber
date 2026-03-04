package connector

import (
	"context"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

// Connector defines the interface all log source connectors must implement.
type Connector interface {
	// Stream opens a long-lived connection and sends raw logs as they arrive.
	Stream(ctx context.Context, cfg ConnectorConfig) (<-chan model.RawLog, error)

	// Query fetches a batch of historical logs matching the given parameters.
	Query(ctx context.Context, cfg ConnectorConfig, params QueryParams) ([]model.RawLog, error)
}

// ConnectorConfig holds provider-specific connection settings.
type ConnectorConfig struct {
	Provider string
	APIKey   string
	Endpoint string
	Extra    map[string]string
}

// QueryParams defines filters for historical log queries.
type QueryParams struct {
	Start time.Time
	End   time.Time
	Limit int
	Filter string
}
