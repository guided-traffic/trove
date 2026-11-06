// The NuGet part of the k6 load of R-HA-059 and R-HA-052 (the drain and failover under NuGet load, Q-HA-100): a constant arrival
// rate through the edge against nuget-all. Together with k6/npm.js (120 requests/s) it makes the 150 requests/s of R-HA-059's
// table: the 20 % of NuGet registration and flat-container GETs are the 30 requests/s of this script. Four in ten requests
// are a registration index, three in ten a flat-container version list, three in ten a package download from the flat
// container. A document is asked with the Accept header of the clients' HTTP stack, and every second round of a VU revalidates
// with the ETag it saw (304). A package is compared with its length and its sha512.
//
// Every request becomes one line on the console, which test/e2e (nuget_load_test.go) parses:
//
//   G <unix ms> <status> <replica> <duration ms> <registration|flat|nupkg> <ok|bad> <path>
//
// `bad` marks a package whose bytes differ from what the repository was filled with. A request without a response has status 0
// and k6's error code and text in the replica column, as in npm.js.
//
// Files in /out: token (tokens that read nuget-all, one per line; never an argument), nuget-fixture.json ({"registration":
// [path...], "flat": [path...], "etags": {path: weak ETag the document had when the test read it}, "nupkgs": [{"path", "size",
// "integrity"}...]}, written by the test). The weak ETag of a rendered document is the same on every replica (R-NUGET-030), so
// the test hands it over and every second round revalidates with it: the 304s do not depend on which VU met the path before.
// Environment: TARGET, HOST, HOSTS_ADDR, DURATION, TIMEOUT as npm.js; RATE (requests/s, default 30: the NuGet share of
// R-HA-059's 150).
import http from 'k6/http';
import exec from 'k6/execution';
import crypto from 'k6/crypto';
import { SharedArray } from 'k6/data';

const TARGET = __ENV.TARGET || 'http://trove-control-plane:30080';
const HOST = __ENV.HOST || 'localhost';
const TOKENS = open('/out/token').split('\n').map((s) => s.trim()).filter((s) => s);
const FIXTURE = new SharedArray('fixture', () => [JSON.parse(open('/out/nuget-fixture.json'))]);

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios: {
    nuget: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 30),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      // as npm.js: VUs are allocated up front, because a request held while the server retries the database occupies one
      preAllocatedVUs: 500,
      maxVUs: 2000,
      gracefulStop: '60s',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

const etags = {};

function line(t0, res, kind, note, path) {
  const replica = res.status === 0 ? `${res.error_code || 0}:${res.error || 'error'}` : (res.headers['X-Trove-Replica'] || '-');
  console.log(`G ${t0} ${res.status} ${String(replica).replace(/\s+/g, '_')} ${Date.now() - t0} ${kind} ${note} ${path}`);
}

export default function () {
  const n = exec.scenario.iterationInTest;
  const f = FIXTURE[0];
  const timeout = __ENV.TIMEOUT || '60s';
  const headers = { Host: HOST, Authorization: `Bearer ${TOKENS[n % TOKENS.length]}` };
  const t0 = Date.now();
  const slot = n % 10;
  if (slot < 7) {
    const kind = slot < 4 ? 'registration' : 'flat';
    const list = slot < 4 ? f.registration : f.flat;
    const path = list[Math.floor(n / 10) % list.length];
    const known = etags[path] || f.etags[path];
    if (known && Math.floor(n / 10) % 2 === 1) {
      headers['If-None-Match'] = known;
    }
    const res = http.get(TARGET + path, { headers, timeout, tags: { name: kind } });
    if (res.status === 200 && res.headers['Etag']) {
      etags[path] = res.headers['Etag'];
    }
    line(t0, res, kind, 'ok', path);
    return;
  }
  const pkg = f.nupkgs[Math.floor(n / 10) % f.nupkgs.length];
  const res = http.get(TARGET + pkg.path, { headers, timeout, responseType: 'binary', tags: { name: 'nupkg' } });
  let note = 'ok';
  if (res.status === 200 && (res.body.byteLength !== pkg.size || crypto.sha512(res.body, 'base64') !== pkg.integrity)) {
    note = 'bad';
  }
  line(t0, res, 'nupkg', note, pkg.path);
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
