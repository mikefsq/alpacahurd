'use strict';
(() => {
  const form = document.getElementById('add-device-form');
  if (!form) return;
  const el = name => document.getElementById('add-' + name);
  let draft = null, selected = null, busy = false;
  const inputs = new Map();
  async function request(path, body) {
    const response = await fetch(path, {method: 'POST', body});
    const result = await response.json();
    if (!response.ok) throw Error(result.error || 'Request failed');
    return result;
  }
  async function run(action) {
    if (busy) return;
    busy = true;
    el('error').hidden = true;
    el('driver').disabled = el('scan').disabled = el('save').disabled = true;
    try { await action(); }
    catch (error) { if (!draft) el('status').textContent = 'Unable to find devices. Correct the problem and try again.'; el('error').textContent = error.message; el('error').hidden = false; }
    finally {
      busy = false;
      el('driver').disabled = false;
      el('scan').disabled = !el('driver').value;
      el('save').disabled = false;
    }
  }
  function choose(index) {
    selected = index;
    const found = index === null ? null : draft.discovery.devices[index];
    const values = {...(draft.values || {}), ...(found?.values || {})};
    inputs.clear(); el('fields').replaceChildren();
    // The scan label ends in a parenthesised serial; the name is the product alone.
    el('name').value = draft.values?.name || (found?.label || '').replace(/ \(.*\)$/, '');
    for (const field of draft.fields || []) {
      const identity = (draft.discovery.identity || []).includes(field.name);
      if (found && identity) continue;
      const label = document.createElement('label'), title = document.createElement('span');
      title.className = 'lab'; title.textContent = field.label || field.name;
      let input;
      if (field.type === 'select') {
        input = document.createElement('select');
        for (const option of field.options || []) input.append(new Option(option, option));
      } else {
        input = document.createElement('input');
        input.type = ['number', 'checkbox', 'password'].includes(field.type) ? field.type : 'text';
        if (input.type === 'number') input.step = 'any';
      }
      const value = values[field.name] ?? (identity ? '' : field.default);
      if (input.type === 'checkbox') input.checked = value === true || value === 'true';
      else input.value = value ?? '';
      // Only deliberate edits are submitted: defaults must not accidentally pin identities.
      input.dataset.changed = 'false';
      input.addEventListener('input', () => { input.dataset.changed = 'true'; });
      const help = document.createElement('small'); help.className = 'help';
      help.textContent = [field.help, field.constraints].filter(Boolean).join(' · ');
      label.append(title, input, help); el('fields').append(label);
      inputs.set(field.name, {input, identity, value});
    }
    el('prototype').textContent = draft.prototype
      ? 'Uses the existing configuration: ' + draft.prototype
      : 'Creates a new configuration with a unique instance ID.';
    el('details').hidden = false;
  }
  async function scan() {
    el('status').textContent = 'Finding devices…';
    el('details').hidden = true; el('results').replaceChildren(); draft = null;
    const body = new URLSearchParams({driver: el('driver').value});
    draft = await request('/setup/add/options', body);
    const scan = draft.discovery;
    const devices = scan.devices || [], identity = scan.identity || [];
    el('status').textContent = scan.supported
      ? (devices.length ? 'Select your device.' : 'No devices found. Check the connection and try again.')
      : (identity.length ? 'Automatic discovery is not available. Enter the connection settings below.' : 'This driver has no per-device hardware selector. Review its settings below.');
    const problems = [scan.error, ...(draft.warnings || []).map(w => 'Warning: ' + w)].filter(Boolean);
    if (problems.length) { el('error').textContent = problems.join('\n'); el('error').hidden = false; }
    for (const [index, found] of devices.entries()) {
      const row = document.createElement('p'), title = document.createElement('strong');
      title.textContent = found.label;
      const values = document.createElement('span');
      values.textContent = ' — ' + Object.entries(found.values || {}).map(([key, value]) => key + ': ' + value).join(', ');
      row.append(title, values, document.createTextNode(' '));
      if (found.configured) {
        const link = document.createElement('a');
        link.textContent = 'Already configured: ' + found.configured;
        link.href = found.configured === '(inline)' ? '/setup' : '/setup/edit?instance=' + encodeURIComponent(found.configured);
        row.append(link);
      } else {
        const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Use this device';
        button.addEventListener('click', () => { if (!busy) choose(index); }); row.append(button);
      }
      el('results').append(row);
    }
    if (!scan.supported) choose(null);
  }
  el('driver').addEventListener('change', () => {
    draft = null; el('details').hidden = true; el('results').replaceChildren();
    el('status').textContent = ''; el('error').hidden = true;
    el('scan').disabled = !el('driver').value;
    if (el('driver').value) run(scan);
  });
  el('scan').addEventListener('click', () => run(scan));
  form.addEventListener('submit', event => {
    event.preventDefault();
    run(async () => {
      if (!draft) throw Error('Choose a driver and find devices first.');
      const body = new URLSearchParams({token: draft.token, name: el('name').value, enable: String(el('enable').checked)});
      if (selected !== null) body.set('device', String(selected));
      for (const [name, {input, identity}] of inputs) {
        const value = input.type === 'checkbox' ? String(input.checked) : input.value;
        if (input.dataset.changed === 'true' || (identity && value !== '')) body.set('field.' + name, value);
      }
      const result = await request('/setup/add', body);
      window.location.assign(result.location);
    });
  });
})();
