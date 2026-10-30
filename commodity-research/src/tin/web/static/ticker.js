(() => {
  function startTicker(viewport) {
    if (viewport.dataset.tickerReady) return;
    const track = viewport.querySelector('[data-ticker-track]');
    const items = viewport.querySelector('[data-ticker-items]');
    if (!track || !items) return;
    viewport.dataset.tickerReady = 'true';

    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');
    let motionReduced = reducedMotion.matches;
    let copy = null, distance = 0, position = 0, frame = null, previousTime = null;
    let hovered = false, focused = false, pressed = false, away = false;
    const canMove = () => distance > 0 && !hovered && !focused && !pressed && !away
      && !document.hidden && !motionReduced;

    function stop() {
      if (frame !== null) window.cancelAnimationFrame(frame);
      frame = null;
      previousTime = null;
    }

    function advance(time) {
      frame = null;
      if (!canMove()) return;
      if (previousTime !== null) {
        // Cap elapsed time after a busy frame so the next quote never jumps past the reader.
        position = (position + Math.min(time - previousTime, 64) * 0.028) % distance;
        viewport.scrollLeft = position;
      }
      previousTime = time;
      frame = window.requestAnimationFrame(advance);
    }

    function resume() {
      stop();
      if (!canMove()) return;
      position = viewport.scrollLeft % distance;
      viewport.scrollLeft = position;
      frame = window.requestAnimationFrame(advance);
    }

    function refresh() {
      stop();
      if (copy) copy.remove();
      copy = null;
      distance = 0;
      const width = items.getBoundingClientRect().width;
      if (viewport.clientWidth > 0 && width > viewport.clientWidth + 1 && !motionReduced) {
        const gap = parseFloat(window.getComputedStyle(track).columnGap) || 0;
        distance = width + gap;
        copy = items.cloneNode(true);
        copy.removeAttribute('data-ticker-items');
        copy.setAttribute('data-ticker-copy', '');
        copy.setAttribute('aria-hidden', 'true');
        track.append(copy);
      }
      resume();
    }

    viewport.addEventListener('mouseenter', () => { hovered = true; stop(); });
    viewport.addEventListener('mouseleave', () => { hovered = false; resume(); });
    viewport.addEventListener('focusin', () => { focused = true; stop(); });
    viewport.addEventListener('focusout', event => {
      focused = viewport.contains(event.relatedTarget);
      resume();
    });
    viewport.addEventListener('pointerdown', () => { pressed = true; stop(); }, {passive: true});
    const release = () => { pressed = false; resume(); };
    window.addEventListener('pointerup', release, {passive: true});
    window.addEventListener('pointercancel', release, {passive: true});
    document.addEventListener('visibilitychange', resume);
    window.addEventListener('pagehide', () => { away = true; stop(); });
    window.addEventListener('pageshow', () => { away = false; pressed = false; refresh(); });
    window.addEventListener('resize', refresh);
    reducedMotion.addEventListener('change', () => {
      // Reading matches in every frame can consume a change before Chromium reports its event.
      motionReduced = reducedMotion.matches;
      refresh();
    });
    if (window.ResizeObserver) {
      const observer = new window.ResizeObserver(refresh);
      observer.observe(viewport);
      observer.observe(items);
    }
    document.fonts?.ready.then(refresh);
    refresh();
  }

  function initialize() {
    document.querySelectorAll('[data-ticker]').forEach(startTicker);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initialize, {once: true});
  else initialize();
})();
