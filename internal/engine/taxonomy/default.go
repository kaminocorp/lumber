package taxonomy

import "github.com/kaminocorp/lumber/internal/model"

// DefaultRoots returns the built-in taxonomy tree that ships with Lumber.
// 42 leaves across 8 roots. Leaf descriptions are the texts that get embedded,
// so they are written for maximum semantic richness and inter-category separation.
func DefaultRoots() []*model.TaxonomyNode {
	return []*model.TaxonomyNode{
		{
			Name: "ERROR",
			Desc: "Application errors, exceptions, and failures",
			Children: []*model.TaxonomyNode{
				// Tuned: added NXDOMAIN, dial tcp, ECONNREFUSED to attract DNS/network entries away from REQUEST.server_error
				{Name: "connection_failure", Desc: "TCP connection refused, ECONNREFUSED, dial tcp failed, DNS resolution NXDOMAIN, network unreachable, TLS handshake error, database connection lost, Redis connection reset", Severity: "error"},
				// Tuned: removed "expired token" (was stealing from ACCESS.session_expired), removed "login" (was stealing from ACCESS.login_failure). Focused on error-path authn failures.
				{Name: "auth_failure", Desc: "Authentication error, invalid credentials, bad username or password, invalid API token, incorrect secret key, signature verification failed, certificate not trusted", Severity: "error"},
				{Name: "authorization_failure", Desc: "Permission denied, forbidden, insufficient scope, access control rejected, RBAC policy violation", Severity: "error"},
				{Name: "timeout", Desc: "Request deadline exceeded, operation timed out waiting for response, context deadline exceeded, gateway timeout", Severity: "error"},
				// Tuned: added "TypeError", "undefined is not" to attract JavaScript exceptions away from validation_error
				{Name: "runtime_exception", Desc: "Unhandled exception, panic, segfault, null pointer dereference, TypeError undefined is not a function, stack overflow, uncaught throw crashed the process", Severity: "error"},
				// Tuned: removed generic "type error" to avoid stealing TypeError exceptions
				{Name: "validation_error", Desc: "Input validation failure, schema mismatch, malformed request body, constraint violation, field must be a valid value, missing required field", Severity: "warning"},
				{Name: "out_of_memory", Desc: "OOM kill, heap exhaustion, memory allocation failure, Java OutOfMemoryError, container memory limit exceeded", Severity: "error"},
				// Tuned: removed "request rejected" which attracted 400 Bad Request entries
				{Name: "rate_limited", Desc: "HTTP 429 Too Many Requests, API throttling, quota exceeded, rate limit counter reached maximum, too many API calls per minute", Severity: "warning"},
				{Name: "dependency_error", Desc: "Upstream service failure, downstream dependency unavailable, circuit breaker open, external API returned error", Severity: "error"},
			},
		},
		{
			Name: "REQUEST",
			Desc: "HTTP requests and API responses",
			Children: []*model.TaxonomyNode{
				{Name: "success", Desc: "HTTP 200 OK response, successful API call completed, request served with 2xx status code", Severity: "info"},
				// Tuned: added "file too large", "payload too big" to attract size-limit 400s away from rate_limited
				{Name: "client_error", Desc: "HTTP 400 Bad Request, 404 Not Found, 422 Unprocessable Entity, client-side 4xx error, file too large, payload exceeds size limit", Severity: "warning"},
				{Name: "server_error", Desc: "HTTP 500 Internal Server Error, 502 Bad Gateway, 503 Service Unavailable, server-side 5xx error response", Severity: "error"},
				{Name: "redirect", Desc: "HTTP 301 Moved Permanently, 302 Found, 307 Temporary Redirect, 3xx redirect response", Severity: "info"},
				{Name: "slow_request", Desc: "Slow HTTP request exceeding latency threshold, high response time, request took longer than expected SLA", Severity: "warning"},
			},
		},
		{
			Name: "DEPLOY",
			Desc: "Deployment and build pipeline events",
			Children: []*model.TaxonomyNode{
				{Name: "build_started", Desc: "CI/CD build process initiated, compilation started, build pipeline triggered", Severity: "info"},
				{Name: "build_succeeded", Desc: "Build completed successfully, compilation finished, all build steps passed", Severity: "info"},
				// Tuned: added "undefined symbol", "cannot find package", "npm install failed" to attract build errors that happen to mention identifiers
				{Name: "build_failed", Desc: "Build failed with errors, compilation error, undefined symbol, cannot find package, npm install failed, CI pipeline broken, build step exited with non-zero code", Severity: "error"},
				{Name: "deploy_started", Desc: "Deployment initiated, rolling out new version, release process started", Severity: "info"},
				{Name: "deploy_succeeded", Desc: "Deployment completed successfully, new version is live, release finished", Severity: "info"},
				{Name: "deploy_failed", Desc: "Deployment failed, release rolled back automatically, deploy step errored", Severity: "error"},
				{Name: "rollback", Desc: "Deployment rollback triggered, reverting to previous version, release rolled back", Severity: "warning"},
			},
		},
		{
			Name: "SYSTEM",
			Desc: "Infrastructure and system-level events",
			Children: []*model.TaxonomyNode{
				// Tuned: added "liveness probe failed", "health check failed", "readiness check failed" to attract failed probes
				{Name: "health_check", Desc: "Kubernetes liveness probe, readiness probe, health check passed or failed, health endpoint /healthz, container probe result, heartbeat ping", Severity: "info"},
				{Name: "scaling_event", Desc: "Autoscale up or down, HPA scaling replicas, instance count changed, horizontal pod autoscaler triggered by CPU utilization, replica adjustment, adding more instances", Severity: "info"},
				{Name: "resource_alert", Desc: "CPU usage threshold breach, memory usage high approaching limit, disk space warning, resource utilization alert, usage percentage exceeded threshold", Severity: "warning"},
				{Name: "process_lifecycle", Desc: "Service started listening on port, process stopped, server restarting, graceful shutdown, SIGTERM received, process crashed, application boot, server initialized with pid", Severity: "info"},
				{Name: "config_change", Desc: "Environment variable updated, feature flag toggled, configuration reloaded, settings changed", Severity: "info"},
			},
		},
		{
			Name: "ACCESS",
			Desc: "Authentication, authorization, and access control events",
			Children: []*model.TaxonomyNode{
				{Name: "login_success", Desc: "Successful user authentication, user logged in, SSO login completed, OAuth token granted", Severity: "info"},
				// Tuned: added "invalid password", "wrong password", "login attempt" to pull login failures away from ERROR.auth_failure
				{Name: "login_failure", Desc: "Failed user login attempt, wrong password entered, invalid password rejected, account locked after repeated failures, MFA verification failed, TOTP code incorrect, user login denied", Severity: "warning"},
				// Tuned: added "expired token" which was removed from ERROR.auth_failure
				{Name: "session_expired", Desc: "User session timed out, JWT token expired, bearer token expiration, session invalidated, cookie expired, refresh token no longer valid", Severity: "info"},
				{Name: "permission_change", Desc: "User role granted or revoked, permission modified, RBAC role assignment changed, access level updated", Severity: "info"},
				{Name: "api_key_event", Desc: "API key created, API key rotated, API key revoked, service account token generated", Severity: "info"},
			},
		},
		{
			Name: "PERFORMANCE",
			Desc: "Performance metrics and degradation events",
			Children: []*model.TaxonomyNode{
				{Name: "latency_spike", Desc: "p50 p95 p99 latency degradation, response time spike, increased request duration percentiles", Severity: "warning"},
				// Tuned: added "requests per second dropped", "QPS decline" to differentiate from latency_spike
				{Name: "throughput_drop", Desc: "Request rate decrease, requests per second dropped, traffic volume drop, QPS decline, fewer queries per second than baseline, reduced throughput", Severity: "warning"},
				{Name: "queue_backlog", Desc: "Job queue growing, message queue consumer lag, pending tasks increasing, worker backlog", Severity: "warning"},
				{Name: "cache_event", Desc: "Cache miss rate increased, cache eviction, cache hit ratio degradation, Redis cache cold start", Severity: "info"},
				// Tuned: added "slow UPDATE", "slow INSERT", "query took too long" to attract slow DML away from DATA.query_executed
				{Name: "db_slow_query", Desc: "Slow database query exceeding time threshold, SQL query took too many seconds, query execution time exceeded limit, long-running database operation above SLA", Severity: "warning"},
			},
		},
		{
			Name: "DATA",
			Desc: "Database and data operations",
			Children: []*model.TaxonomyNode{
				// Tuned: added "query completed normally" to emphasize routine execution, not slow/problematic
				{Name: "query_executed", Desc: "Database query execution log, SQL statement completed normally, routine SELECT INSERT UPDATE DELETE, query returned rows, statement finished", Severity: "info"},
				{Name: "migration", Desc: "Database schema migration, table altered, column added, migration script applied, schema version updated", Severity: "info"},
				{Name: "replication", Desc: "Data replication event, database sync, replica caught up, backup completed, data export finished", Severity: "info"},
			},
		},
		{
			Name: "SCHEDULED",
			Desc: "Cron jobs and scheduled tasks",
			Children: []*model.TaxonomyNode{
				{Name: "cron_started", Desc: "Scheduled job started, cron task triggered, periodic task began execution", Severity: "info"},
				{Name: "cron_completed", Desc: "Scheduled job completed successfully, cron task finished, periodic task done", Severity: "info"},
				// Tuned: added "scheduled task error", "cron job error", "periodic job crashed" to attract failed crons even when error details mention other domains
				{Name: "cron_failed", Desc: "Scheduled job failed with error, cron task crashed, periodic job execution failure, cron job exited with error, scheduled task did not complete", Severity: "error"},
			},
		},
	}
}
