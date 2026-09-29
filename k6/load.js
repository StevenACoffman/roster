// Load profile for the roster service.
//
// The Baggage header is the point of this script: k6 does not send one on its
// own, so without it K6LabelsMiddleware is present but inert and profiles cannot
// be sliced by run or scenario. The server turns `k6.*` baggage into pprof
// labels, so a flame graph in Pyroscope can be filtered to
// `k6_scenario="curate"`.
//
// This asserts load behaviour — status codes, throughput, error rate — and
// deliberately not correctness. Scoping, paging and concurrency semantics are
// asserted by the integration suite against a real database; a second set of
// assertions here would drift from it and there would be no way to tell which
// was authoritative.
//
//   just load-test
//   just load-test 2m
import http from 'k6/http';
import { check, fail } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const DURATION = __ENV.DURATION || '30s';
const SERVICE = 'oneroster.v1p2.v1.RosterService';

// One id per run, so successive runs stay distinguishable in Pyroscope.
const RUN_ID = __ENV.RUN_ID || `local-${Date.now()}`;

// Authentication. In development the server is started with --dev-subject and
// this header selects which principal to act as, which is how a load test can
// exercise the authorization joins rather than always hitting one scope. Against
// a server using real tokens, set ROSTER_LOAD_TOKEN instead.
const TOKEN = __ENV.ROSTER_LOAD_TOKEN || '';
const DEV_SUBJECT = __ENV.DEV_SUBJECT || 'oidc|dee';

export const options = {
  scenarios: {
    browse: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 5),
      duration: DURATION,
      exec: 'browse',
    },
    curate: {
      // Fewer VUs than browse: curation is a human activity and a realistic
      // profile should not make writes the dominant load.
      executor: 'constant-vus',
      vus: 2,
      duration: DURATION,
      exec: 'curate',
    },
  },
  thresholds: {
    checks: ['rate>0.99'],
    http_req_failed: ['rate<0.01'],
    // Separate latency budgets: a write does more work than a read, and one
    // combined threshold would hide a slow read behind a fast write.
    'http_req_duration{scenario:browse}': ['p(95)<500'],
    'http_req_duration{scenario:curate}': ['p(95)<1000'],
  },
};

function params(scenario) {
  const headers = {
    'Content-Type': 'application/json',
    Baggage: `k6.test_run_id=${RUN_ID},k6.scenario=${scenario}`,
  };
  if (TOKEN) {
    headers.Authorization = `Bearer ${TOKEN}`;
  } else {
    headers['X-Roster-Dev-Subject'] = DEV_SUBJECT;
  }
  return { headers, tags: { scenario } };
}

function rpc(method, body, scenario) {
  return http.post(`${BASE}/${SERVICE}/${method}`, JSON.stringify(body), params(scenario));
}

export function browse() {
  const orgs = rpc('GetAllOrgs', { pageSize: 20 }, 'browse');
  if (!check(orgs, { 'GetAllOrgs 200': (r) => r.status === 200 })) return;

  const list = orgs.json('orgs') || [];
  if (list.length === 0) return;

  const org = list[Math.floor(Math.random() * list.length)];
  check(rpc('GetOrg', { sourcedId: org.sourcedId }, 'browse'), {
    'GetOrg 200': (r) => r.status === 200,
  });

  // The join-heavy reads: classes attach their terms and users attach their
  // roles, each through a batched association query. These are the paths where
  // an N+1 regression would show up as latency rather than as a wrong answer.
  check(rpc('GetAllClasses', { pageSize: 20 }, 'browse'), {
    'GetAllClasses 200': (r) => r.status === 200,
  });
  check(rpc('GetAllUsers', { pageSize: 20 }, 'browse'), {
    'GetAllUsers 200': (r) => r.status === 200,
  });

  // Follow the cursor when there is one, so paging is under load too.
  const next = orgs.json('nextPageToken');
  if (next) {
    check(rpc('GetAllOrgs', { pageSize: 20, pageToken: next }, 'browse'), {
      'GetAllOrgs page 2 200': (r) => r.status === 200,
    });
  }
}

export function curate() {
  // A school to hang the new records off. Without one there is nothing in scope
  // to curate, and every write would correctly be refused.
  const schools = rpc('GetAllSchools', { pageSize: 1 }, 'curate');
  if (!check(schools, { 'GetAllSchools 200': (r) => r.status === 200 })) return;

  const list = schools.json('orgs') || [];
  if (list.length === 0) return;
  const school = list[0].sourcedId;

  const suffix = `${__VU}-${__ITER}-${Math.floor(Math.random() * 1e6)}`;

  const created = rpc(
    'CreateCourse',
    {
      course: {
        sourcedId: `load-course-${suffix}`,
        status: 'active',
        title: `Load Course ${suffix}`,
        courseCode: `LOAD${suffix}`,
        org: { sourcedId: school, href: `/orgs/${school}`, type: 'org' },
      },
    },
    'curate',
  );
  if (!check(created, { 'CreateCourse 200': (r) => r.status === 200 })) return;

  const sourcedId = created.json('course.sourcedId');
  const version = created.json('course.dateLastModified');
  if (!sourcedId) fail('CreateCourse returned no sourcedId');
  if (!version) fail('CreateCourse returned no dateLastModified to use as a version');

  // A masked partial update — the path the field mask and the concurrency token
  // both exist for.
  const updated = rpc(
    'UpdateCourse',
    {
      course: { sourcedId, title: `Renamed ${suffix}` },
      updateMask: 'title',
      expectedDateLastModified: version,
    },
    'curate',
  );
  check(updated, { 'UpdateCourse 200': (r) => r.status === 200 });

  // Soft delete, so the load test does not accumulate rows across runs.
  check(rpc('DeleteCourse', { sourcedId }, 'curate'), {
    'DeleteCourse 200': (r) => r.status === 200,
  });
}
