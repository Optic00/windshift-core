# Prometheus metrics

Windshift exposes Prometheus metrics from the public `GET /metrics` endpoint.
When `WINDSHIFT_CONTEXT_PATH` is set, the endpoint uses the same prefix as the
rest of the application, for example `/windshift/metrics`.

The endpoint is intentionally unauthenticated so an infrastructure scraper can
reach it in the same way as `/healthz` and `/readyz`. Restrict it at the reverse
proxy or network boundary when metrics should not be available publicly.

The registry includes:

- Go runtime metrics (`go_*`), including goroutines, heap use, and GC pauses.
- Process metrics (`windshift_process_*`), including CPU, memory, and open file
  descriptors where the operating system supports them.
- Database pool metrics (`go_sql_*`) for open, in-use, and idle connections,
  connection waits, and connection lifetime closures.
- HTTP request totals and duration histograms (`windshift_http_*`). The `route`
  label contains the registered route pattern, such as `/api/items/{id}`, and
  never the raw request path. Both metric families also include the response
  status code.
- Agent run queue depth, in-flight runs, retained outcomes, and average duration
  (`windshift_agent_*`).
- Retained webhook delivery outcomes (`windshift_webhook_dispatches`).
- Scheduled SCM poll totals, failures, and duration (`windshift_scm_*`).

Example Prometheus configuration:

```yaml
scrape_configs:
  - job_name: windshift
    static_configs:
      - targets: [windshift:8080]
```
