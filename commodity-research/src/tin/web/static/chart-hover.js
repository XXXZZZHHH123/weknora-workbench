/* Shared, non-modal chart previews. Link navigation remains native. */
(function (root) {
  'use strict';
  const cache = new Map(), bindings = new WeakMap();
  const noHover = root.matchMedia('(hover: none)');
  let pending = null, current = null, restoringFocus = false, dragging = false;

  function cancelPending() {
    if (pending) root.clearTimeout(pending.timer);
    pending = null;
  }
  function close() {
    cancelPending();
    const previous = current;
    current = null;
    if (previous) {
      previous.renderer?.destroy();
      previous.dialog.remove();
    }
  }
  function place(view) {
    const margin = 16, gap = 12, box = view.trigger.getBoundingClientRect();
    const width = document.documentElement.clientWidth, height = root.innerHeight;
    view.dialog.style.width = Math.max(1, Math.min(662, width - margin * 2)) + 'px';
    const panel = view.dialog.getBoundingClientRect();
    let left = box.right + gap, top = box.top;
    if (left + panel.width > width - margin) {
      left = box.left - panel.width - gap;
      if (left < margin) { left = box.left; top = box.bottom + gap; }
    }
    left = Math.max(margin, Math.min(left, width - panel.width - margin));
    top = Math.max(margin, Math.min(top, height - panel.height - margin));
    // Keep the document position on scroll; the panel moves with its trigger.
    view.dialog.style.left = root.scrollX + left + 'px';
    view.dialog.style.top = root.scrollY + top + 'px';
  }
  function read(options) {
    if (!cache.has(options.key)) {
      const request = Promise.resolve().then(() => options.load());
      cache.set(options.key, request);
      request.catch(() => { if (cache.get(options.key) === request) cache.delete(options.key); });
    }
    return cache.get(options.key);
  }
  async function show(trigger, options) {
    close();
    if (noHover.matches || dragging || !trigger.isConnected || !root.ChartKit) return;
    const dialog = document.createElement('div'), plot = document.createElement('div');
    const title = options.title || trigger.textContent.trim() || '图表';
    dialog.className = 'chart-hover';
    dialog.setAttribute('role', 'dialog'); dialog.setAttribute('aria-label', title);
    plot.className = 'chart-hover-plot';
    const status = document.createElement('p');
    status.className = 'chart-hover-status'; status.setAttribute('role', 'status'); status.textContent = '正在读取…';
    plot.append(status); dialog.append(plot); document.body.append(dialog);
    const view = {trigger, dialog, plot, renderer: null};
    current = view; place(view);
    try {
      const value = await read(options);
      // A closed or replaced preview must never render a late response.
      if (current !== view) return;
      const chartTitle = value.title || title;
      dialog.setAttribute('aria-label', chartTitle); plot.replaceChildren();
      view.renderer = root.ChartKit.render(plot, value.data, {
        definition: value.definition, title: chartTitle,
        width: Math.min(640, plot.clientWidth), height: 320
      });
      place(view);
    } catch (error) {
      if (current !== view) return;
      plot.replaceChildren(status); status.classList.add('error');
      status.textContent = error.message || '图表读取失败，请重试。'; place(view);
    }
  }
  function attach(trigger, options) {
    bindings.get(trigger)?.();
    if (noHover.matches) return () => {};
    let hovered = false, focused = false;
    function schedule() {
      if (restoringFocus || noHover.matches || dragging) return;
      cancelPending();
      if (current?.trigger === trigger) return;
      const request = {trigger, timer: root.setTimeout(() => {
        if (pending === request) { pending = null; show(trigger, options); }
      }, 250)};
      pending = request;
    }
    function leave() {
      if (!hovered && !focused && pending?.trigger === trigger) cancelPending();
    }
    const enter = event => { if (event.pointerType !== 'touch') { hovered = true; schedule(); } };
    const exit = () => { hovered = false; leave(); };
    const focus = () => { focused = true; schedule(); };
    const blur = () => { focused = false; leave(); };
    trigger.addEventListener('pointerenter', enter); trigger.addEventListener('pointerleave', exit);
    trigger.addEventListener('focus', focus); trigger.addEventListener('blur', blur);
    const detach = () => {
      trigger.removeEventListener('pointerenter', enter); trigger.removeEventListener('pointerleave', exit);
      trigger.removeEventListener('focus', focus); trigger.removeEventListener('blur', blur);
      if (current?.trigger === trigger) close();
      else if (pending?.trigger === trigger) cancelPending();
      bindings.delete(trigger);
    };
    bindings.set(trigger, detach);
    return detach;
  }
  document.addEventListener('pointerdown', event => {
    if (current?.dialog.contains(event.target) || current?.trigger.contains(event.target)) return;
    if (!current && pending?.trigger.contains(event.target)) return;
    close();
  }, true);
  document.addEventListener('keydown', event => {
    if (event.key !== 'Escape') return;
    const trigger = current?.trigger;
    close();
    if (trigger?.isConnected) {
      event.preventDefault(); restoringFocus = true;
      try { trigger.focus({preventScroll: true}); } finally { restoringFocus = false; }
    }
  });
  document.addEventListener('dragstart', event => {
    dragging = true; close();
    // A card can cancel dragging while saving; cancelled drags have no dragend.
    root.setTimeout(() => { if (event.defaultPrevented) dragging = false; }, 0);
  }, true);
  document.addEventListener('dragend', () => { dragging = false; }, true);
  root.addEventListener('resize', () => { if (current) place(current); });
  root.addEventListener('pagehide', close);
  noHover.addEventListener('change', () => { if (noHover.matches) close(); });
  root.ChartHover = Object.freeze({attach, close});
})(window);
