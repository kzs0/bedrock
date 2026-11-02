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
  const rows = [...cases].sort(([a], [b]) => a.localeCompare(b)).map(([name, event]) => {
    const duration = Number.isFinite(event.Elapsed) ? `${event.Elapsed.toFixed(2)}s` : '—';
    return `| \`${name}\` | ${event.Action} | ${duration} |`;
  });
  const version = /^go\d+\.\d+(?:\.\d+)?$/.test(goVersion ?? '') ? goVersion.replace(/^go/, 'Go ') : 'version unavailable';
  const lines = [
    `### Functional E2E results (${version})`, '',
    `**${passed ? 'Passed' : 'Failed or incomplete'}** · Linked-library E2E coverage: **${percentage}**`, '',
    'Real application subprocesses and local HTTP/gRPC receivers validate public behavior.',
    'Coverage excludes fixture/example code and includes only linked library packages. It is separate from unit coverage.', '',
    '| Scenario | Result | Duration |', '|---|---|---:|',
    ...(rows.length ? rows : ['| Results unavailable | incomplete | — |']), '',
    `[Logs and coverage inventories](${runUrl})`,
  ];
  if (!passed) {
    const tail = log.trim().replace(/`/g, "'").slice(-6000) || 'No E2E diagnostics were produced.';
    lines.push('', '<details><summary>E2E diagnostics</summary>', '', '```text', tail, '```', '</details>');
  }
  return { passed, markdown: lines.join('\n') };
}

module.exports = { renderE2EReport };
