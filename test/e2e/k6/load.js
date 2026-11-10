// The client load of E0.13, E0.14 and E0.15 (R-HA-051): requests at RATE a second, alternating
// between GET /api/v1/system/ping and GET /api/v1/system/replicas, through the Ingress.
//
// LOOP=open (default) is a constant arrival rate: the requests start on a clock whatever the answers do, so a request that the
// server holds for retry.max_elapsed (a failover, a drain) is held next to the 100 that start every second. LOOP=closed is RATE
// clients, each starting its next request one second after the last one started, or when the last one ended if that is later: at most
// RATE requests a second and never more than RATE open connections and requests. When the edge or the pods stop answering, the
// closed loop slows down where the open loop piles up (up to 2,000 connections and, once the stall ends, a burst), so a stall of the
// runner is not made worse by the test (F-119, Q-HA-288). The rotation row of E0.15 is the closed loop; a test that holds requests
// on purpose is the open one.
//
// Every response becomes one line on the console, which test/e2e parses:
//
//   R <unix ms> <status> <replica> <duration ms> <path> [<client number>]
//
// The client number is there for LOOP=closed only; test/e2e uses it to show that every client ran through a window.
//
// A request that never got a response has status 0 and k6's error code and text (<code>:<text>, spaces as _) in the replica column.
// k6's own summary (handleSummary) goes to /out/summary.json as a cross-check.
//
// Environment: TARGET (base URL, default http://trove-control-plane:30080, the edge's NodePort on the
// kind Docker network; https:// for a TLS edge, whose CA the container gets in SSL_CERT_FILE), HOST (the
// Host header, and the server name of a TLS edge, default localhost), HOSTS_ADDR (optional host:port
// that HOST resolves to, for a TLS edge whose certificate names HOST while TARGET names the node), RATE
// (requests/s, or clients for LOOP=closed, default 100), LOOP (open or closed, default open), DURATION (default 60s), TIMEOUT (per request, default 15s; the tests pass 45s, longer than retry.max_elapsed, so a held request ends as the server's answer and not as a client timeout).
import http from 'k6/http';
import { sleep } from 'k6';
import exec from 'k6/execution';

const TARGET = __ENV.TARGET || 'http://trove-control-plane:30080';
const HOST = __ENV.HOST || 'localhost';
const PATHS = ['/api/v1/system/ping', '/api/v1/system/replicas'];

const RATE = Number(__ENV.RATE || 100);
const CLOSED = (__ENV.LOOP || 'open') === 'closed';

const openLoop = {
  executor: 'constant-arrival-rate',
  rate: RATE,
  timeUnit: '1s',
  duration: __ENV.DURATION || '60s',
  preAllocatedVUs: 2000,  // a request that the server holds while it retries the database occupies a VU; a VU started on demand is slow (TLS handshake included) and k6 would drop iterations, so the worst case is allocated up front: 50 requests/s held for retry.max_elapsed (30 s) is 1500 VUs
  maxVUs: 4500,  // a request held for retry.max_elapsed (30 s) occupies a VU: 100/s x 30 s
  gracefulStop: '30s',
};

const closedLoop = {
  executor: 'constant-vus',
  vus: RATE,
  duration: __ENV.DURATION || '60s',
  gracefulStop: '30s',
};

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios: { load: CLOSED ? closedLoop : openLoop },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  if (!CLOSED) {
    request();
    return;
  }
  // The clients start spread over the first second, not all at once, and keep their offset: a client waits out the rest of its second
  // after a request like one that was served quickly.
  if (exec.vu.iterationInScenario === 0) {
    sleep((exec.vu.idInTest % RATE) / RATE);
  }
  const began = Date.now();
  request(exec.vu.idInTest);
  sleep(Math.max(0, 1 - (Date.now() - began) / 1000));
}

function request(vu) {
  const path = PATHS[exec.scenario.iterationInTest % PATHS.length];
  const t0 = Date.now();
  const res = http.get(TARGET + path, { headers: { Host: HOST }, timeout: __ENV.TIMEOUT || '15s', tags: { name: path } });
  // A request without a response carries k6's error code and text in the replica column: <code>:<text>.
  const replica = res.status === 0 ? `${res.error_code || 0}:${res.error || 'error'}` : (res.headers['X-Trove-Replica'] || '-');
  console.log(`R ${t0} ${res.status} ${String(replica).replace(/\s+/g, '_')} ${Date.now() - t0} ${path}${vu === undefined ? '' : ' ' + vu}`);
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
