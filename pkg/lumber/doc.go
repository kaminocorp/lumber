// Package lumber provides a log classification engine that embeds log text
// into vectors and classifies against a 42-label taxonomy.
//
// Quick start (auto-download, recommended for getting started):
//
//	l, err := lumber.New(lumber.WithAutoDownload())
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer l.Close()
//
//	event, _ := l.Classify("ERROR: connection refused to db-primary:5432")
//	fmt.Println(event.Type, event.Category) // ERROR connection_failure
//
// WithAutoDownload fetches ~35-60MB of model files on first call, cached at
// ~/.cache/lumber (or $LUMBER_CACHE_DIR). Subsequent calls are instant.
//
// For production or Docker deployments, use pre-downloaded models:
//
//	l, err := lumber.New(lumber.WithModelDir("/opt/lumber/models"))
//
// The Lumber instance is safe for concurrent use. Create once, reuse across
// requests. See the README for full documentation.
package lumber
