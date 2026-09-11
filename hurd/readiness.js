(() => {
  const cells = [...document.querySelectorAll('[data-readiness]')];
  const refresh = document.getElementById('refresh-readiness');
  if (!cells.length || !refresh) return;
  async function update(cell) {
    const badge = cell.querySelector('.readiness-label');
    const start = cell.closest('tr').querySelector('[data-start]');
    if (start) start.disabled = true;
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch('/setup/readiness?instance=' + encodeURIComponent(cell.dataset.readiness), {signal:controller.signal, cache:'no-store'});
      if (!response.ok) throw new Error('Check unavailable');
      const result = await response.json();
      if (start) {
        start.disabled = result.canStart !== true;
        start.title = start.disabled ? 'Enable the device and validate its configuration to start it' : 'Start the driver process';
      }
      badge.textContent = result.state;
      badge.className = 'readiness-label ' + (['ok','warn','error','neutral'].includes(result.kind) ? result.kind : 'neutral');
    } catch (error) {
      badge.textContent = (badge.textContent.startsWith('Disabled') ? 'Disabled' : 'Enabled') + ' · Not verified';
      badge.className = 'readiness-label neutral';
    } finally { clearTimeout(timeout); }
  }
  async function run() {
    refresh.disabled = true;
    refresh.textContent = 'Checking…';
    let next = 0;
    await Promise.all(Array.from({length:Math.min(3,cells.length)}, async () => {
      while (next < cells.length) await update(cells[next++]);
    }));
    refresh.disabled = false;
    refresh.textContent = 'Refresh status';
  }
  refresh.addEventListener('click', run);
  run();
})();

// Keep overlays outside the table: mobile browsers can clip fixed descendants.
(() => {
  const menus = [...document.querySelectorAll('.device-menu')];
  const panels = new Map();
  function close(menu) {
    menu.open = false;
    const panel = panels.get(menu);
    if (panel.hasAttribute('popover') && panel.matches(':popover-open')) panel.hidePopover();
    panel.hidden = true;
  }
  const closeAll = () => menus.forEach(close);
  function updateViewport() {
    const viewport = window.visualViewport;
    const height = Math.max(window.innerHeight, document.documentElement.clientHeight);
    const bottom = viewport ? Math.max(0, height - viewport.height - viewport.offsetTop) : 0;
    document.documentElement.style.setProperty('--viewport-bottom', bottom + 'px');
  }
  function place(menu) {
    updateViewport();
    const mobile = window.matchMedia('(max-width:700px)').matches;
    const panel = panels.get(menu);
    const anchor = mobile ? menu.closest('tr') : menu.querySelector('summary');
    const rect = anchor.getBoundingClientRect();
    const viewport = window.visualViewport;
    const left = viewport ? viewport.offsetLeft : 0;
    const top = viewport ? viewport.offsetTop : 0;
    const width = viewport ? viewport.width : window.innerWidth;
    const height = viewport ? viewport.height : window.innerHeight;
    if (mobile) panel.style.width = Math.min(rect.width, width - 16) + 'px';
    else panel.style.removeProperty('width');
    panel.style.left = Math.max(left + 8, Math.min(mobile ? rect.left : rect.right - panel.offsetWidth, left + width - panel.offsetWidth - 8)) + 'px';
    const below = rect.bottom + 4;
    const above = rect.top - panel.offsetHeight - 4;
    panel.style.top = Math.max(top + 8, Math.min(below + panel.offsetHeight <= top + height - 8 ? below : above, top + height - panel.offsetHeight - 8)) + 'px';

  }
  for (const [index, menu] of menus.entries()) {
    const panel = menu.querySelector('.device-menu-panel');
    const trigger = menu.querySelector('summary');
    panels.set(menu, panel);
    panel.hidden = true;
    panel.id = 'device-actions-' + index;
    trigger.setAttribute('aria-controls', panel.id);
    document.body.appendChild(panel);
    // Use the browser's top layer when available, above page compositing layers.
    if (typeof panel.showPopover === 'function') panel.setAttribute('popover', 'manual');
    let timer;
    const contains = node => menu.contains(node) || panel.contains(node);
    const open = () => {
      clearTimeout(timer);
      menus.forEach(other => { if (other !== menu) close(other); });
      menu.open = true;
      panel.hidden = false;
      if (panel.hasAttribute('popover') && !panel.matches(':popover-open')) panel.showPopover();
      place(menu);
    };
    menu.addEventListener('toggle', () => { if (menu.open) open(); else close(menu); });
    menu.addEventListener('pointerenter', event => { if (event.pointerType === 'mouse') open(); });
    for (const element of [menu, panel]) {
      element.addEventListener('pointerenter', () => clearTimeout(timer));
      element.addEventListener('pointerleave', event => {
        if (event.pointerType === 'mouse') timer = setTimeout(() => {
          if (!contains(document.activeElement)) close(menu);
        }, 150);
      });
      // Touch focus changes can precede the button click; do not close on blur.
      element.addEventListener('keydown', event => {
        if (event.key === 'Escape') { close(menu); trigger.focus(); event.preventDefault(); }
        if (event.key === 'Tab') setTimeout(() => {
          if (!contains(document.activeElement)) close(menu);
        }, 0);
      });
    }
    trigger.addEventListener('keydown', event => {
      if (event.key === 'Tab' && !event.shiftKey && menu.open) {
        const first = panel.querySelector('button:not(:disabled)');
        if (first) { first.focus(); event.preventDefault(); }
      }
    });
  }
  document.addEventListener('pointerdown', event => {
    if (!event.target.closest('.device-menu, .device-menu-panel')) closeAll();
  });
  const reposition = () => { updateViewport(); menus.forEach(menu => { if (menu.open) place(menu); }); };
  window.addEventListener('resize', reposition);
  window.addEventListener('scroll', () => {
    if (window.matchMedia('(max-width:700px)').matches) reposition(); else closeAll();
  }, true);
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', reposition);
    window.visualViewport.addEventListener('scroll', reposition);
  }
  updateViewport();
})();

(() => {
  const filter = document.getElementById('device-state-filter');
  if (!filter) return;
  const rows = [...document.querySelectorAll('tr[data-device-enabled]')];
  const apply = () => {
    for (const row of rows) {
      row.hidden = filter.value !== 'all' && row.dataset.deviceEnabled !== String(filter.value === 'enabled');
      if (row.hidden) row.querySelectorAll('details[open]').forEach(menu => { menu.open = false; });
    }
    try { sessionStorage.setItem('alpacahurd-device-filter', filter.value); } catch (_) {}
  };
  try {
    const saved = sessionStorage.getItem('alpacahurd-device-filter');
    if (['all', 'enabled', 'disabled'].includes(saved)) filter.value = saved;
  } catch (_) {}
  filter.addEventListener('change', apply);
  apply();
})();
