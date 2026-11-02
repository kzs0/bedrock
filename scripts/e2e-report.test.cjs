const { test } = require('node:test');
const assert = require('node:assert/strict');
const { renderE2EReport } = require('./e2e-report.cjs');
const event = value => JSON.stringify({ Package: 'example/compat/otlp/e2e', ...value });
const success = [
  'make command and download diagnostics',
  event({ Test: 'TestProfiles', Action: 'pass', Elapsed: 1.23 }),
  event({ Test: 'TestProfiles/heap', Action: 'pass' }),
  event({ Action: 'pass' }),
].join('\n');
const config = { log: success, coverage: 'total: (statements) 62.6%\n', exitCode: '0', goVersion: 'go1.24.13', runUrl: 'https://github.com/example/actions/runs/1' };

test('renders top-level scenarios and separate coverage', () => {
  const report = renderE2EReport(config);
  assert.equal(report.passed, true);
  assert.match(report.markdown, /Profiles.*pass.*1\.23s/);
  assert.doesNotMatch(report.markdown, /TestProfiles\/heap/);
  assert.equal(report.coverage, '62.6%');
  assert.match(report.markdown, /Go 1\.24\.13/);
  assert.match(report.markdown, /<summary>E2E scenarios · 1 passed/);
  assert.doesNotMatch(report.markdown, /<details open>/);
});

test('does not hide failing executions behind passing events', () => {
  const report = renderE2EReport({ ...config, exitCode: '2' });
  assert.equal(report.passed, false);
  assert.match(report.markdown, /E2E diagnostics/);
});

test('rejects missing, unfinished and empty results', () => {
  for (const override of [{ log: '' }, { coverage: '' }, { exitCode: undefined }, { log: event({ Test: 'TestProfiles', Action: 'pass' }) }]) {
    assert.equal(renderE2EReport({ ...config, ...override }).passed, false);
  }
});

test('reports failed scenarios with available diagnostics', () => {
  const log = [event({ Test: 'TestProfiles', Action: 'fail' }), event({ Action: 'fail' })].join('\n');
  const report = renderE2EReport({ ...config, log });
  assert.equal(report.passed, false);
  assert.match(report.markdown, /Profiles.*fail/);
});

test('does not substitute another package for e2e results', () => {
  assert.equal(renderE2EReport({ ...config, log: success.replaceAll('example/compat/otlp/e2e', 'example/unit') }).passed, false);
});
