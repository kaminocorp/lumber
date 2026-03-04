# Phase 6, Section 3: Expanded Real-World Test Corpus — Completion Notes

**Completed:** 2026-02-24
**Phase:** 6 (Beta Validation & Polish)
**Depends on:** Section 2 (empty-input handling affects how edge cases are treated in the pipeline)

## Summary

Added 49 real-world-patterned log entries to the test corpus, expanding it from 104 to 153 entries. The new entries cover production log formats not represented in the original synthetic corpus: nginx/ALB access logs, Python tracebacks, Kubernetes events, AWS CloudWatch alarms, Docker JSON logs, systemd journal, gRPC errors, Terraform output, Helm release notes, MySQL/PostgreSQL slow query log formats, and more.

All 42 taxonomy leaves remain covered. Corpus validation tests pass. ONNX accuracy testing deferred to an environment with the model downloaded.

## What Changed

### Modified Files

| File | Lines changed | What |
|------|---------------|------|
| `internal/engine/testdata/corpus.json` | +49 entries | Real-world-patterned log entries |

### New Entries by Category (49 total)

| Category | Count | Patterns added |
|----------|-------|----------------|
| ERROR.connection_failure | 2 | AWS RDS ECONNRESET, MongoDB replica set timeout |
| ERROR.auth_failure | 1 | mTLS x509 certificate verification |
| ERROR.authorization_failure | 1 | Kubernetes RBAC forbidden (Go log format) |
| ERROR.timeout | 1 | gRPC DeadlineExceeded |
| ERROR.runtime_exception | 2 | Python traceback with AttributeError, Node.js unhandled promise rejection |
| ERROR.validation_error | 1 | GraphQL BAD_USER_INPUT |
| ERROR.out_of_memory | 1 | Node.js JavaScript heap OOM |
| ERROR.rate_limited | 1 | OpenAI API 429 rate limit |
| ERROR.dependency_error | 1 | gRPC UNAVAILABLE with circuit breaker, key=value format |
| REQUEST.success | 2 | Nginx combined log format HTTP/2, AWS ALB access log |
| REQUEST.client_error | 2 | Nginx 404 combined format, Express.js 413 payload too large |
| REQUEST.server_error | 1 | 504 gateway timeout JSON |
| REQUEST.redirect | 1 | HSTS 307 redirect |
| REQUEST.slow_request | 1 | Slow POST response JSON |
| DEPLOY.build_started | 1 | GitHub Actions workflow |
| DEPLOY.build_succeeded | 1 | Docker image build success |
| DEPLOY.build_failed | 2 | Docker pip install failure, Webpack/Babel syntax error |
| DEPLOY.deploy_started | 1 | ArgoCD automated sync JSON |
| DEPLOY.deploy_succeeded | 1 | Helm upgrade release notes |
| DEPLOY.deploy_failed | 1 | Kubernetes progress deadline exceeded |
| DEPLOY.rollback | 1 | kubectl rollout undo |
| SYSTEM.health_check | 1 | AWS ELB health check |
| SYSTEM.scaling_event | 1 | AWS ASG EC2 instance launch JSON |
| SYSTEM.resource_alert | 1 | Prometheus alerting rule (PromQL format) |
| SYSTEM.process_lifecycle | 2 | Systemd journal, Docker JSON log |
| SYSTEM.config_change | 1 | Terraform apply output |
| ACCESS.login_success | 1 | OAuth2 callback JSON |
| ACCESS.login_failure | 1 | Account locked after repeated failures |
| ACCESS.session_expired | 1 | Refresh token revoked |
| ACCESS.permission_change | 1 | AWS IAM policy attached JSON |
| ACCESS.api_key_event | 1 | API key expiry warning |
| PERFORMANCE.latency_spike | 1 | Datadog APM alert JSON |
| PERFORMANCE.throughput_drop | 1 | Load balancer connection drop |
| PERFORMANCE.queue_backlog | 1 | AWS SQS CloudWatch alarm JSON |
| PERFORMANCE.cache_event | 1 | Memcached eviction rate spike |
| PERFORMANCE.db_slow_query | 2 | PostgreSQL slow query log, MySQL slow query log |
| DATA.query_executed | 1 | GORM ORM query log |
| DATA.migration | 1 | Flyway schema migration |
| DATA.replication | 1 | Replica lag warning |
| SCHEDULED.cron_started | 1 | Kubernetes CronJob |
| SCHEDULED.cron_completed | 1 | Celery beat task |
| SCHEDULED.cron_failed | 1 | Kubernetes CronJob exceeded deadline |

### Format Diversity

The new entries add these production log formats not in the original corpus:

| Format | Examples |
|--------|----------|
| Nginx combined log | `10.0.1.50 - frank [24/Feb/...] "GET /assets/main.css HTTP/2" 200 ...` |
| AWS ALB access log | `{"elb":"app/prod-alb/...", "target_status_code":200, ...}` |
| Kubernetes Go-style log | `W0224 08:15:00.000000 1 authorization.go:73] Forbidden: ...` |
| Docker JSON log | `{"log":"\\u001b[36minfo\\u001b[39m: Server listening...", "stream":"stdout"}` |
| Systemd journal | `Feb 24 08:30:01 ip-10-0-1-42 systemd[1]: Started api-server.service` |
| PostgreSQL slow log | `LOG:  duration: 8542.301 ms  statement: SELECT ...` |
| MySQL slow log | `# Time: ...\n# User@Host: ...\n# Query_time: ...\nSELECT ...` |
| Python traceback | `Traceback (most recent call last):\n  File ...` |
| Prometheus alert | `FIRING [HighMemoryUsage] — memory_usage_percent{...} = 93.2 > threshold 90` |
| Terraform output | `Apply complete! Resources: 2 added, 1 changed, 0 destroyed.` |
| Helm release notes | `Release "api-server" has been upgraded. Happy Helming!` |
| gRPC error | `rpc error: code = DeadlineExceeded desc = ...` |
| Celery beat | `[celery.beat] INFO Task analytics.tasks.aggregate_daily_stats sent ...` |

## Design Decisions

- **1-2 entries per category, not 5-10** — the goal is format diversity, not volume. Each new entry adds a production log pattern that's semantically distinct from existing entries.
- **Entries are clearly classifiable** — each log contains strong signal for its target category. Ambiguous logs (where reasonable people would disagree on classification) are avoided — the corpus is for validation, not adversarial testing.
- **Multi-line logs included** — Python tracebacks, MySQL slow query logs, and Helm output span multiple lines. These stress-test the tokenizer's handling of `\n` in input.
- **Real service names used** — Stripe, Datadog, ArgoCD, Terraform, Celery — because production logs reference specific tools and the model should handle vendor-specific language.

## ONNX Accuracy Testing

The ONNX model is not available in this environment. The `TestCorpusAccuracy` test will run when the model is present. If any of the 49 new entries misclassify:

1. Check the confidence score — if close to threshold (0.5-0.6), the entry may be genuinely ambiguous
2. Identify the confusion pair — which category did it land in instead?
3. Tune the taxonomy description in `internal/engine/taxonomy/default.go` — add discriminating keywords to the correct category, remove shared language from the confused category
4. Re-run accuracy test to verify

Based on Phase 3 experience (89% → 100% in 3 rounds), description tuning should resolve any misclassifications quickly.

## Verification

```
go test -v github.com/kaminocorp/lumber/internal/engine/testdata   # 3 tests pass (153 entries, 42 leaves covered)
go test ./...                                                        # full suite passes
go test -v -run TestCorpus ./internal/engine/...                     # accuracy test (requires ONNX model)
```
