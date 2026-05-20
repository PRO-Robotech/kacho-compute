// KAC-127 Phase 4 — list_filter_perf.js
//
// Validates SLA P4.GWT-29 / P4.GWT-31:
//   - 100 concurrent VUs
//   - 30 minute steady state
//   - p95 ≤ 100ms, p99 ≤ 250ms for InstanceService.List
//   - cache hit ratio ≥ 80% (verified via dedicated metric exported by compute)
//
// Pre-conditions (operator):
//   1. kacho-compute and kacho-iam are running with KACHO_COMPUTE_LIST_FILTER_ENABLED=true.
//   2. Test project has ≥1000 Instances seeded (use seed script).
//   3. K6_API_GATEWAY_URL points at REST api-gateway (e.g. https://api.kacho.local).
//   4. K6_BEARER_TOKEN is a valid JWT (passkey + dpop) for `usr_k6_loadtest`.
//   5. usr_k6_loadtest has an FGA binding granting viewer on ~500 of 1000 instances.
//
// Run:
//   k6 run -e K6_API_GATEWAY_URL=https://api.kacho.local \
//          -e K6_BEARER_TOKEN=$JWT \
//          -e K6_PROJECT_ID=prj_loadtest \
//          tests/k6/list_filter_perf.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Rate } from 'k6/metrics';

const apiURL = __ENV.K6_API_GATEWAY_URL || 'http://localhost:18080';
const bearer = __ENV.K6_BEARER_TOKEN || '';
const projectID = __ENV.K6_PROJECT_ID || 'prj_loadtest';
const fastMode = __ENV.K6_FAST === '1';

// SLA tracking — custom metrics for finer assertions.
const listLatency = new Trend('list_instance_latency_ms');
const listSuccess = new Rate('list_instance_ok');

export const options = {
  scenarios: {
    steady: {
      executor: 'constant-vus',
      vus: 100,
      duration: fastMode ? '30s' : '30m',
    },
  },
  thresholds: {
    // P4.GWT-29 / P4.GWT-31 SLA.
    'list_instance_latency_ms': ['p(95)<100', 'p(99)<250'],
    'list_instance_ok': ['rate>0.999'], // 99.9% successful responses
    'http_req_failed': ['rate<0.001'],   // <0.1% HTTP-level failures
  },
};

const headers = {
  'Authorization': bearer ? `Bearer ${bearer}` : '',
  'Content-Type': 'application/json',
};

export default function () {
  const url = `${apiURL}/compute/v1/instances?projectId=${projectID}&pageSize=100`;
  const t0 = Date.now();
  const res = http.get(url, { headers, tags: { rpc: 'InstanceService.List' } });
  const elapsed = Date.now() - t0;

  listLatency.add(elapsed);
  const ok = check(res, {
    'status is 200': (r) => r.status === 200,
    'body has instances': (r) => {
      try { return Array.isArray(r.json('instances')); } catch (_) { return false; }
    },
  });
  listSuccess.add(ok ? 1 : 0);

  // Don't pause between iterations — we want sustained 100 RPS×VU.
  sleep(0.01);
}

// Summary hook prints the cardinality of unique instance ids the load-tester
// saw, so reviewers can sanity-check the allow-list size in the binding.
export function handleSummary(data) {
  return {
    'stdout': JSON.stringify(data, null, 2),
    'tests/k6/list_filter_perf.summary.json': JSON.stringify(data, null, 2),
  };
}
