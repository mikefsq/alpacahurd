(() => {
  const form = document.getElementById('config-editor');
  if (!form) return;
  const text = form.elements.namedItem('text');
  const check = document.getElementById('check-config');
  const save = document.getElementById('save-config');
  const syntaxStatus = document.getElementById('syntax-status');
  const configStatus = document.getElementById('config-status');
  let revision = 0;
  let checkedText = null;
  let request = null;

  // Strip JSONC comments while preserving strings, escapes and line positions.
  function stripComments(source) {
    const out = source.split('');
    let quoted = false;
    let escaped = false;
    for (let i = 0; i < out.length; i++) {
      const c = out[i];
      if (quoted) {
        if (escaped) escaped = false;
        else if (c === '\\') escaped = true;
        else if (c === '"') quoted = false;
      } else if (c === '"') {
        quoted = true;
      } else if (c === '/' && out[i + 1] === '/') {
        while (i < out.length && out[i] !== '\n') out[i++] = ' ';
      } else if (c === '/' && out[i + 1] === '*') {
        out[i++] = ' ';
        out[i++] = ' ';
        while (i + 1 < out.length && !(out[i] === '*' && out[i + 1] === '/')) {
          if (out[i] !== '\n') out[i] = ' ';
          i++;
        }
        if (i + 1 >= out.length) throw new Error('Unclosed block comment.');
        out[i] = out[++i] = ' ';
      }
    }
    return out.join('');
  }

  function status(element, kind, message) {
    element.className = 'banner ' + kind;
    element.textContent = message;
  }

  function changed() {
    revision++;
    if (request) request.abort();
    request = null;
    checkedText = null;
    save.disabled = true;
    status(configStatus, '', 'Configuration has not been checked.');
    try {
      const value = JSON.parse(stripComments(text.value));
      if (!value || Array.isArray(value) || typeof value !== 'object') {
        throw new Error('Configuration must be a JSON object.');
      }
      status(syntaxStatus, 'ok', 'JSON syntax valid.');
      check.disabled = false;
    } catch (error) {
      status(syntaxStatus, 'error', 'Invalid JSON: ' + error.message);
      check.disabled = true;
    }
  }

  text.addEventListener('input', changed);
  check.addEventListener('click', async () => {
    const version = revision;
    const draft = text.value;
    const controller = new AbortController();
    request = controller;
    const timeout = setTimeout(() => controller.abort(), 10000);
    check.disabled = true;
    save.disabled = true;
    checkedText = null;
    status(configStatus, '', 'Checking configuration…');
    try {
      const response = await fetch('/setup/edit/check', {
        method: 'POST',
        body: new URLSearchParams({instance: form.elements.namedItem('instance').value, text: draft}),
        signal: controller.signal
      });
      const result = await response.json();
      if (version !== revision) return;
      if (!response.ok || result.valid !== true) throw new Error(result.message || 'Configuration check failed.');
      checkedText = draft;
      save.disabled = false;
      status(configStatus, result.ready === false ? 'warn' : 'ok', result.message);
    } catch (error) {
      if (version !== revision) return;
      status(configStatus, 'error', error.name === 'AbortError'
        ? 'Configuration check timed out. Try again.'
        : 'Could not validate: ' + error.message);
    } finally {
      clearTimeout(timeout);
      if (version === revision) {
        check.disabled = false;
        request = null;
      }
    }
  });
  form.addEventListener('submit', event => {
    if (checkedText === null || checkedText !== text.value || save.disabled) {
      event.preventDefault();
      changed();
      status(configStatus, 'warn', 'Check the current configuration before saving.');
      return;
    }
    save.disabled = true;
    status(configStatus, '', 'Verifying and saving…');
  });
  window.addEventListener('pageshow', changed);
  changed();
})();
