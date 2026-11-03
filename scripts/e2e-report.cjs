const finished = new Set(['pass', 'fail', 'skip']);

function renderE2EReport({ log = '', coverage = '', exitCode, runUrl, goVersion }) {
  const cases = new Map();
  let packagePassed = false;
  for (const line of log.split('\n')) {
    let event;
    try { event = JSON.parse(line); } catch { continue; }
    if (!event.Package?.endsWith('/e2e')) continue;
    if (!event.Test && finished.has(event.Action)) packagePassed = event.Action === 'pass';
    if (/^Test\w+$/.test(event.Test ?? '') && finished.has(event.Action)) {
      cases.set(event.Test, event);
    }
  }
  const match = coverage.match(/^total:\s+\(statements\)\s+(\d+(?:\.\d+)?)%\s*$/m);
  const percentage = match && Number(match[1]) <= 100 ? `${match[1]}%` : 'Unavailable';
  const passed = String(exitCode) === '0' && packagePassed && cases.size > 0
    && ![...cases.values()].some(event => event.Action === 'fail') && percentage !== 'Unavailable';
  const count = action => [...cases.values()].filter(event => event.Action === action).length;
  const passedCount = count('pass');
  const failedCount = count('fail');
  const skippedCount = count('skip');
  const rows = [...cases].sort(([a], [b]) => a.localeCompare(b)).map(([name, event]) => {
    const duration = Number.isFinite(event.Elapsed) ? `${event.Elapsed.toFixed(2)}s` : '—';
    const label = name.replace(/^Test/, '').replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2').replace(/([a-z0-9])([A-Z])/g, '$1 $2');
    return `| ${label} | ${event.Action} | ${duration} |`;
  });
  const version = /^go\d+\.\d+(?:\.\d+)?$/.test(goVersion ?? '') ? goVersion.replace(/^go/, 'Go ') : 'version unavailable';
  const result = passed ? `${passedCount} passed` : (failedCount ? `${failedCount} failed` : 'Incomplete results');
  const lines = [
    passed ? '<details>' : '<details open>',
    `<summary>E2E scenarios · ${result}${skippedCount ? ` · ${skippedCount} skipped` : ''}</summary>`, '',
    `Coverage run: ${version} · real application processes and HTTP/gRPC receivers.`, '',
    '| Scenario | Result | Duration |', '|---|---|---:|',
    ...(rows.length ? rows : ['| Results unavailable | incomplete | — |']), '',
    '</details>',
  ];
  if (!passed) {
    const tail = log.trim().replace(/`/g, "'").slice(-6000) || 'No E2E diagnostics were produced.';
    lines.push('', '<details><summary>E2E diagnostics</summary>', '', '```text', tail, '```', '</details>');
  }
  return { passed, coverage: percentage, version, passedCount, failedCount, skippedCount, scenarioCount: cases.size, markdown: lines.join('\n') };
}

module.exports = { renderE2EReport };
