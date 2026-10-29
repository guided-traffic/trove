// The client load of E0.13 and E0.14 (R-HA-051): a constant arrival rate of requests, alternating
// between GET /api/v1/system/ping and GET /api/v1/system/replicas, through the Ingress.
//
// Every response becomes one line on the console, which test/e2e parses:
//
//   R <unix ms> <status> <replica> <duration ms> <path>
//
// A request that never got a response has status 0 and the k6 error code in the replica column.
// k6's own summary (handleSummary) goes to /out/summary.json as a cross-check.
//
// Environment: TARGET (base URL, default http://trove-control-plane:30080, the edge's NodePort on the
// kind Docker network; https:// for a TLS edge, whose CA the container gets in SSL_CERT_FILE), HOST (the
// Host header, and the server name of a TLS edge, default localhost), HOSTS_ADDR (optional host:port
// that HOST resolves to, for a TLS edge whose certificate names HOST while TARGET names the node), RATE
// (requests/s, default 100), DURATION (default 60s), TIMEOUT (per request, default 15s; the tests pass 45s, longer than retry.max_elapsed, so a held request ends as the server's answer and not as a client timeout).
import http from 'k6/http';
import exec from 'k6/execution';

const TARGET = __ENV.TARGET || 'http://trove-control-plane:30080';
const HOST = __ENV.HOST || 'localhost';
const PATHS = ['/api/v1/system/ping', '/api/v1/system/replicas'];

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios: {
    load: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 100),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      preAllocatedVUs: 2000,  // a request that the server holds while it retries the database occupies a VU; a VU started on demand is slow (TLS handshake included) and k6 would drop iterations, so the worst case is allocated up front: 50 requests/s held for retry.max_elapsed (30 s) is 1500 VUs
      maxVUs: 4500,  // a request held for retry.max_elapsed (30 s) occupies a VU: 100/s x 30 s
      gracefulStop: '30s',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const path = PATHS[exec.scenario.iterationInTest % PATHS.length];
  const t0 = Date.now();
  const res = http.get(TARGET + path, { headers: { Host: HOST }, timeout: __ENV.TIMEOUT || '15s', tags: { name: path } });
  const replica = res.status === 0 ? (res.error_code || res.error || 'error') : (res.headers['X-Trove-Replica'] || '-');
  console.log(`R ${t0} ${res.status} ${String(replica).replace(/\s+/g, '_')} ${Date.now() - t0} ${path}`);
}

export function handleSummary(data) {
  const m = (name, key) => (data.metrics[name] && data.metrics[name].values ? data.metrics[name].values[key] : null);
  return {
    '/out/summary.json': JSON.stringify({
      http_reqs: m('http_reqs', 'count'),
      dropped_iterations: m('dropped_iterations', 'count') || 0,
      http_req_failed_rate: m('http_req_failed', 'rate'),
      duration_ms: { avg: m('http_req_duration', 'avg'), p95: m('http_req_duration', 'p(95)'), p99: m('http_req_duration', 'p(99)'), max: m('http_req_duration', 'max') },
    }, null, 2),
  };
}
