// The targets of R-ARCH-027 that an npm client meets, as k6 scenarios against one web replica (WP2.8). They run one
// after the other, each for DURATION (default 30s):
//
//   p1  P-1  a local tarball of at most 1 MiB, time to first byte            50 requests/s   p50 <= 25 ms, p99 <= 100 ms
//   p3  P-3  a package document of npm-local from the rendition cache, gzip  50 requests/s   p50 <= 5 ms, p99 <= 25 ms
//   p5  P-5  a package document of the virtual repository npm-all            50 requests/s   p99 <= 200 ms
//   p7  P-7  package documents from the rendition cache                      2,000 requests/s  p99 <= 50 ms, rate held
//   p8  P-8  a cached 50 KiB tarball                                          1,000 requests/s  rate held
//
// P-5 asks for a merge that is not cached; a client cannot see whether the replica merged, so p5 is the virtual
// repository's document as it is served. P-4, P-6 and P-9 to P-17 need the replica's side or another driver.
// "Measured at the replica": the load generator runs inside the cluster and talks to one pod's listener, without the
// edge. GATE=1 makes the targets k6 thresholds (k6 exits 99 when one is missed); that is the run on the reference
// environment of R-ARCH-027 (roadmap E5.3). Without it the script reports, as on the dev profile (Q-ROAD-9).
//
// Files in /out: tokens (one token per line; R-API-016's package_read budget is 200/s per token, so the throughput
// scenarios spread over all of them), perf-fixture.json ({"tarball_1mib", "tarball_50kib", "local_documents": [...],
// "virtual_documents": [...]}, paths below the target). The result is /out/perf-npm.json.
// Environment: TARGET (https://localhost:8080), HOST (localhost), HOSTS_ADDR (<pod IP>:8080, what HOST resolves to),
// DURATION, GATE.
import http from 'k6/http';
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';
import { SharedArray } from 'k6/data';

const TARGET = __ENV.TARGET || 'https://localhost:8080';
const HOST = __ENV.HOST || 'localhost';
const DURATION = __ENV.DURATION || '30s';
const SECONDS = parseInt(DURATION, 10);
const TOKENS = new SharedArray('tokens', () => open('/out/tokens').split('\n').map((s) => s.trim()).filter((s) => s));
const FIXTURE = new SharedArray('fixture', () => [JSON.parse(open('/out/perf-fixture.json'))]);
const ACCEPT = 'application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*';

const SCENARIOS = {
  p1: { rate: 50, exec: 'p1' },
  p3: { rate: 50, exec: 'p3' },
  p5: { rate: 50, exec: 'p5' },
  p7: { rate: 2000, exec: 'p7' },
  p8: { rate: 1000, exec: 'p8' },
};
const trend = {};
const done = {};
const failed = {};
for (const id of Object.keys(SCENARIOS)) {
  trend[id] = new Trend(`${id}_ms`, true);
  done[id] = new Counter(`${id}_requests`);
  failed[id] = new Counter(`${id}_failed`);
}

const scenarios = {};
let start = 0;
for (const [id, s] of Object.entries(SCENARIOS)) {
  scenarios[id] = {
    executor: 'constant-arrival-rate', exec: s.exec, rate: s.rate, timeUnit: '1s', duration: DURATION,
    startTime: `${start}s`, preAllocatedVUs: Math.max(50, s.rate / 2), maxVUs: s.rate * 2, gracefulStop: '10s',
  };
  start += SECONDS + 15;
}

const targets = {
  p1_ms: ['p(50)<=25', 'p(99)<=100'],
  p3_ms: ['p(50)<=5', 'p(99)<=25'],
  p5_ms: ['p(99)<=200'],
  p7_ms: ['p(99)<=50'],
  p7_failed: ['count==0'],
  p8_failed: ['count==0'],
  dropped_iterations: ['count==0'],
};

export const options = {
  hosts: __ENV.HOSTS_ADDR ? { [HOST]: __ENV.HOSTS_ADDR } : {},
  scenarios,
  thresholds: __ENV.GATE === '1' ? targets : {},
  summaryTrendStats: ['med', 'p(99)', 'max'],
};

function get(id, path, binary, ttfb) {
  const headers = { Host: HOST, Authorization: `Bearer ${TOKENS[exec.scenario.iterationInTest % TOKENS.length]}` };
  if (!binary) {
    headers.Accept = ACCEPT;
    headers['Accept-Encoding'] = 'gzip';
  }
  const res = http.get(TARGET + path, { headers, timeout: '30s', responseType: binary ? 'binary' : 'text', tags: { name: id } });
  done[id].add(1);
  if (res.status !== 200) {
    failed[id].add(1);
    return;
  }
  trend[id].add(ttfb ? res.timings.waiting : res.timings.duration);
}

const pick = (list) => list[exec.scenario.iterationInTest % list.length];
export function p1() { get('p1', FIXTURE[0].tarball_1mib, true, true); }
export function p3() { get('p3', pick(FIXTURE[0].local_documents), false, false); }
export function p5() { get('p5', pick(FIXTURE[0].virtual_documents), false, false); }
export function p7() { get('p7', pick(FIXTURE[0].local_documents), false, false); }
export function p8() { get('p8', FIXTURE[0].tarball_50kib, true, false); }

export function handleSummary(data) {
  const v = (name, key) => (data.metrics[name] && data.metrics[name].values ? data.metrics[name].values[key] : null);
  const out = { gate: __ENV.GATE === '1', duration_s: SECONDS, dropped_iterations: v('dropped_iterations', 'count') || 0, scenarios: {} };
  for (const [id, s] of Object.entries(SCENARIOS)) {
    out.scenarios[id] = {
      offered_per_s: s.rate, served_per_s: (v(`${id}_requests`, 'count') || 0) / SECONDS, failed: v(`${id}_failed`, 'count') || 0,
      p50_ms: v(`${id}_ms`, 'med'), p99_ms: v(`${id}_ms`, 'p(99)'), max_ms: v(`${id}_ms`, 'max'),
    };
  }
  const thresholds = {};
  for (const [name, m] of Object.entries(data.metrics)) {
    if (m.thresholds) {
      thresholds[name] = Object.fromEntries(Object.entries(m.thresholds).map(([k, t]) => [k, t.ok]));
    }
  }
  out.thresholds = thresholds;
  return { '/out/perf-npm.json': JSON.stringify(out, null, 2), stdout: `${JSON.stringify(out.scenarios)}\n` };
}
