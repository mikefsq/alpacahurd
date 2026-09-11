(() => {
  const filter = document.getElementById('log-filter');
  if (filter) {
    document.getElementById('log-instance').addEventListener('change', () => filter.requestSubmit());
  }
  const viewer = document.getElementById('log-viewer');
  if (!viewer) return;
  const output = document.getElementById('log-output');
  const status = document.getElementById('log-status');
  const pause = document.getElementById('log-pause');
  const refresh = document.getElementById('log-refresh');
  const follow = document.getElementById('log-follow');
  const url = '/setup/logs/tail?' + new URLSearchParams({instance: viewer.dataset.instance});
  let paused = false;
  let timer = null;
  let request = null;
  let revision = 0;
  let lastUpdate = '';

  function stop() {
    revision++;
    clearTimeout(timer);
    if (request) request.abort();
    request = null;
    refresh.disabled = false;
  }
  function note(message, failed = false) {
    status.textContent = message;
    status.className = failed ? 'sub log-error' : 'sub';
  }
  async function load() {
    if (request || document.hidden) return;
    clearTimeout(timer);
    const version = revision;
    const controller = new AbortController();
    request = controller;
    refresh.disabled = true;
    const timeout = setTimeout(() => controller.abort(), 8000);
    try {
      const response = await fetch(url, {cache: 'no-store', signal: controller.signal});
      const result = await response.json();
      if (version !== revision) return;
      if (!response.ok) throw new Error(result.error || 'Could not read logs.');
      const serviceState = document.getElementById('log-service-state');
      if (serviceState) serviceState.textContent = result.serviceState || 'Unknown';
      const position = output.scrollTop;
      const text = result.text || 'No log messages yet.';
      if (output.textContent !== text) output.textContent = text;
      output.scrollTop = follow.checked ? output.scrollHeight : position;
      lastUpdate = 'Updated ' + new Date(result.updated).toLocaleTimeString();
      note((paused ? 'Paused · ' : '') + lastUpdate);
    } catch (error) {
      if (version !== revision) return;
      note((error.name === 'AbortError' ? 'Log request timed out.' : error.message)
        + (lastUpdate ? ' Showing the previous snapshot. ' + lastUpdate : '')
        + (paused ? '' : ' Retrying automatically.'), true);
    } finally {
      clearTimeout(timeout);
      if (version === revision) {
        request = null;
        refresh.disabled = false;
        if (!paused && !document.hidden) timer = setTimeout(load, 2000);
      }
    }
  }
  pause.disabled = false;
  pause.addEventListener('click', () => {
    paused = !paused;
    stop();
    pause.textContent = paused ? 'Resume' : 'Pause';
    if (paused) note('Paused' + (lastUpdate ? ' · ' + lastUpdate : ''));
    else load();
  });
  refresh.addEventListener('click', load);
  follow.addEventListener('change', () => {
    if (follow.checked) output.scrollTop = output.scrollHeight;
  });
  document.addEventListener('visibilitychange', () => {
    stop();
    if (!document.hidden && !paused) load();
  });
  window.addEventListener('pagehide', stop);
  window.addEventListener('pageshow', () => { if (!paused) load(); });
  load();
})();
