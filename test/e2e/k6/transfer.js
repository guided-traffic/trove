// The raw-transfer load of E1.13 (R-HA-011, R-ARCH-024): RATE clients, each looping an iteration that uploads a
// file to generic-local with PUT /files/e2e/generic-local/..., downloads it again and compares the SHA-256 of what came back, through
// the Ingress. Run next to load.js in the drain test of E0.14.
//
// The loop is closed: a client starts its next iteration when the last one has ended and at the earliest one second after the
// last one started, so RATE clients make at most RATE iterations a second and never more. An open loop (a constant arrival
// rate) starts its iterations on a clock whatever the answers do, and when the edge or the pods stop answering for ten seconds
// it releases the iterations that piled up in one burst; that burst is more than the 50 requests per 5 s that the write
// class of R-API-016 admits for one principal, and the limiter refuses it with a 429 that no drain caused (F-119, Q-HA-230).
//
// Every request becomes one line on the console, which test/e2e parses:
//
//   T <unix ms> <op> <status> <replica> <duration ms> <path> <ok|bad>
//
// op is put or get; `bad` marks a download whose bytes differ from the upload. A request that never got a response
// has status 0 and k6's error code and text in the replica column, as in load.js.
//
// The paths cycle through 40 names, so the repository holds 40 files however long the test runs; the bodies are
// random, so an overwrite is a new blob. The token is the file /out/token (a service account with write rights on
// generic-local), not an argument: a command line is visible to every process of the machine.
//
// Environment: TARGET, HOST, HOSTS_ADDR (as load.js), RATE (clients, which is iterations/s when the answers are quick;
// default 4), DURATION, TIMEOUT, SIZE (bytes of each body, default 262144).
import http from 'k6/http';
import { sleep } from 'k6';
import exec from 'k6/execution';
import crypto from 'k6/crypto';

const TARGET = __ENV.TARGET || 'http://trove-control-plane:30080';
const HOST = __ENV.HOST || 'localhost';
const SIZE = Number(__ENV.SIZE || 262144);
const TOKEN = open('/out/token').trim();

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios: {
    transfer: {
      executor: 'constant-vus',
      vus: Number(__ENV.RATE || 4),
      duration: __ENV.DURATION || '60s',
      gracefulStop: '60s',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

function line(op, t0, res, path, note) {
  const replica = res.status === 0 ? `${res.error_code || 0}:${res.error || 'error'}` : (res.headers['X-Trove-Replica'] || '-');
  console.log(`T ${t0} ${op} ${res.status} ${String(replica).replace(/\s+/g, '_')} ${Date.now() - t0} ${path} ${note}`);
  if (res.status === 429) {
    // not a transfer line (the prefix is H): what the refusal said, for the reader of the artifact
    const h = res.headers;
    console.log(`H ${t0} ${op} 429 RateLimit-Policy=${h['Ratelimit-Policy'] || h['RateLimit-Policy'] || '-'} RateLimit=${h['Ratelimit'] || h['RateLimit'] || '-'} Retry-After=${h['Retry-After'] || '-'} ${String(res.body).slice(0, 160).replace(/\s+/g, ' ')}`);
  }
}

export default function () {
  const began = Date.now();
  transfer();
  // Also after a failed PUT: a client that is refused waits out its second like one that was served.
  sleep(Math.max(0, 1 - (Date.now() - began) / 1000));
}

function transfer() {
  const n = exec.scenario.iterationInTest;
  // One path per iteration: two iterations on one path overlap when a PUT is slow (a drain held one for 10.7 s on CI), and the
  // GET of the first then reads the second's bytes, which is the last writer winning and not a wrong download.
  const path = `/files/e2e/generic-local/e2e-drain/${exec.vu.idInTest}-${n}.bin`;
  const body = crypto.randomBytes(SIZE);
  const want = crypto.sha256(body, 'hex');
  const headers = { Host: HOST, Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/octet-stream' };
  const timeout = __ENV.TIMEOUT || '60s';

  let t0 = Date.now();
  const put = http.put(TARGET + path, body, { headers, timeout, tags: { name: 'put' } });
  line('put', t0, put, path, 'ok');
  if (put.status !== 200 && put.status !== 201) {
    return;
  }
  t0 = Date.now();
  const get = http.get(TARGET + path, { headers, timeout, responseType: 'binary', tags: { name: 'get' } });
  let note = 'ok';
  if (get.status === 200 && crypto.sha256(get.body, 'hex') !== want) {
    note = 'bad';
  }
  line('get', t0, get, path, note);
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
