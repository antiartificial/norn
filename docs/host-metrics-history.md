# Host metrics history

`GET /api/v1/host/metrics/history` requires an authenticated `api:read` principal.
The `host-metrics-history` capability advertises `hostMetricsHistory`.
Supply RFC3339 `start` and `end`, optional positive integer `step` seconds and
`includeServices=true`. Requests are limited to the last 30 days, 720 points per
series, and twelve application/process series ranked by peak CPU or memory share.

The API reads fixed queries from a passing `norn-prometheus-web` Consul service.
Operators can instead configure `NORN_HOST_METRICS_PROMETHEUS_URL` as a trusted
HTTP(S) origin without credentials, query, or path. No caller credentials are
forwarded, redirects are refused, requests are cancelled after 12 seconds, and
upstream response size is bounded. `NORN_HOST_METRICS_HOSTNAME` overrides the
local OS hostname used to select native Nomad host metrics.

Native host CPU averages per-core Nomad utilization; memory is Nomad used/total.
Service CPU rates and working-set memory are relative to native host capacity.
Service history is restricted to the locally registered
`norn-cadvisor-docker-stats-metrics` exporter address, including prior dynamic
ports at that address. Moving the exporter to another address can leave gaps in
older service history; the API never silently combines other hosts.

Minimum sample spacing is 15 seconds in the recent hour, 60 seconds through a
day, and 300 seconds for older ranges, increased as needed to bound point count.
Coarser samples preserve maxima over each bucket, evaluated at 15-second
resolution. CPU and memory peaks may occur at different moments within a bucket.
Clients should fetch visible ranges progressively, use the returned step, and
label the source rather than mix these samples with different live collectors.

`retentionSeconds` is the 30-day API horizon, not a promise that every sample is
available: Prometheus time/size retention, downtime and exporter gaps determine
actual availability. Empty arrays are valid; unavailable monitoring produces
503 rather than fabricated zeroes. There is no additional server history cache.
