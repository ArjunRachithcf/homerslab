// What the Actions runner takes as workflow commands in a step's output, after actions/runner's ActionCommandManager
// and ActionCommand (#115), for the tests: lines split at \n, \r\n and a lone \r (StreamReader.ReadLine); the v2 form
// `::name args::data` after TrimStart, then the v1 form `##[name args]data` anywhere in the line, each only for a
// registered command; while commands are stopped, only the stop token is registered, and it resumes them.
const REGISTERED = ['add-mask', 'add-matcher', 'add-path', 'debug', 'echo', 'endgroup', 'error', 'group', 'notice',
  'remove-matcher', 'save-state', 'set-env', 'set-output', 'stop-commands', 'warning', 'internal-set-repo-path'];

function v2(line, registered) {
  const s = line.trimStart();
  if (!s.startsWith('::')) return null;
  const end = s.indexOf('::', 2);
  if (end < 0) return null;
  const name = s.slice(2, end).split(' ')[0].toLowerCase();
  return registered.has(name) ? { name, data: s.slice(end + 2), line } : null;
}

function v1(line, registered) {
  const at = line.indexOf('##[');
  if (at < 0) return null;
  const close = line.indexOf(']', at);
  if (close < 0) return null;
  const name = line.slice(at + 3, close).split(' ')[0].toLowerCase();
  return registered.has(name) ? { name, data: line.slice(close + 1), line } : null;
}

/** Every command the runner would run for `output`: { name, data, line }, with name 'resume' for the stop token. */
export function commands(output) {
  const found = [];
  let token = null;
  for (const line of output.split(/\r\n|\r|\n/)) {
    const registered = new Set(token === null ? REGISTERED : [token.toLowerCase()]);
    const c = v2(line, registered) ?? v1(line, registered);
    if (!c) continue;
    if (token !== null) {
      found.push({ ...c, name: 'resume' });
      token = null;
    } else {
      found.push(c);
      if (c.name === 'stop-commands') token = c.data;
    }
  }
  return found;
}
