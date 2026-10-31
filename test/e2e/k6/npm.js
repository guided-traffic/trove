// The npm part of the k6 load of R-HA-059 and R-HA-052 (WP2.8, E2.11): a constant arrival rate through the edge
// against npm-all. Five in eight requests are package documents, three in eight are tarballs of 1 to 5 MB: the
// 50 % and 30 % rows of R-HA-059, whose 20 % of NuGet requests join with M3. A document is asked with the Accept
// header of `npm install`, and every second round of a VU revalidates with the ETag it saw (304). A tarball is
// compared with its length and its sha512 integrity.
//
// Every request becomes one line on the console, which test/e2e (npm_load_test.go) parses:
//
//   N <unix ms> <status> <replica> <duration ms> <document|tarball> <ok|bad> <path>
//
// `bad` marks a tarball whose bytes differ from what was published. A request without a response has status 0 and
// k6's error code and text in the replica column, as in load.js.
//
// Files in /out: token (a token that reads npm-all; never an argument), npm-fixture.json ({"documents": [path...],
// "tarballs": [{"path", "size", "integrity"}...]}, written by the test).
// Environment: TARGET, HOST, HOSTS_ADDR, DURATION, TIMEOUT as load.js; RATE (requests/s, default 120: the npm share
// of R-HA-059's 150).
import http from 'k6/http';
import exec from 'k6/execution';
import crypto from 'k6/crypto';
import { SharedArray } from 'k6/data';

const TARGET = __ENV.TARGET || 'http://trove-control-plane:30080';
const HOST = __ENV.HOST || 'localhost';
const TOKEN = open('/out/token').trim();
const FIXTURE = new SharedArray('fixture', () => [JSON.parse(open('/out/npm-fixture.json'))]);
const ACCEPT = 'application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*';

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios: {
    npm: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 120),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      // as load.js: a request the server holds while it retries the database occupies a VU for up to
      // database.retry.max_elapsed, and a VU started on demand is slow, so they are allocated up front
      preAllocatedVUs: 1500,
      maxVUs: 4000,
      gracefulStop: '60s',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

const etags = {};

function line(t0, res, kind, note, path) {
  const replica = res.status === 0 ? `${res.error_code || 0}:${res.error || 'error'}` : (res.headers['X-Trove-Replica'] || '-');
  console.log(`N ${t0} ${res.status} ${String(replica).replace(/\s+/g, '_')} ${Date.now() - t0} ${kind} ${note} ${path}`);
}

export default function () {
  const n = exec.scenario.iterationInTest;
  const f = FIXTURE[0];
  const timeout = __ENV.TIMEOUT || '60s';
  const headers = { Host: HOST, Authorization: `Bearer ${TOKEN}` };
  const t0 = Date.now();
  if (n % 8 < 5) {
    const path = f.documents[n % f.documents.length];
    headers.Accept = ACCEPT;
    if (etags[path] && Math.floor(n / 8) % 2 === 1) {
      headers['If-None-Match'] = etags[path];
    }
    const res = http.get(TARGET + path, { headers, timeout, tags: { name: 'document' } });
    if (res.status === 200 && res.headers['Etag']) {
      etags[path] = res.headers['Etag'];
    }
    line(t0, res, 'document', 'ok', path);
    return;
  }
  const tb = f.tarballs[n % f.tarballs.length];
  const res = http.get(TARGET + tb.path, { headers, timeout, responseType: 'binary', tags: { name: 'tarball' } });
  let note = 'ok';
  if (res.status === 200 && (res.body.byteLength !== tb.size || `sha512-${crypto.sha512(res.body, 'base64')}` !== tb.integrity)) {
    note = 'bad';
  }
  line(t0, res, 'tarball', note, tb.path);
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
