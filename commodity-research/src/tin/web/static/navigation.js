// Keep ordinary server navigation and its authorization checks. Feedback stays
// on the current document while the next page prepares its verified data.
(function () {
  const status = document.getElementById('research-navigation-status');
  const progress = document.getElementById('research-navigation-progress');
  const main = document.querySelector('main.research-main');
  if (!status || !progress || !main) return;
  let reveal, slow;
  function reset() {
    clearTimeout(reveal); clearTimeout(slow);
    status.hidden = true; progress.hidden = true;
    main.removeAttribute('aria-busy');
    document.querySelectorAll('a[data-research-loading]').forEach(link => link.removeAttribute('data-research-loading'));
  }
  document.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    const link = event.target.closest?.('a.nav-btn');
    if (!link || link.hasAttribute('download') || (link.target && link.target !== '_self')) return;
    const next = new URL(link.href, location.href);
    const prefix = window.RESEARCH_PREFIX || '';
    if (next.origin !== location.origin || (prefix && !next.pathname.startsWith(prefix + '/'))
        || (!prefix && !next.pathname.startsWith('/sn') && next.pathname !== '/')
        || (next.pathname === location.pathname && next.search === location.search && next.hash)) return;
    reset();
    main.setAttribute('aria-busy', 'true');
    link.setAttribute('data-research-loading', 'true');
    reveal = setTimeout(() => {
      status.textContent = `正在打开${link.textContent.trim()}…`;
      status.hidden = false; progress.hidden = false;
    }, 120);
    slow = setTimeout(() => {
      status.textContent = '页面加载时间较长，可再次点击导航重试。';
    }, 20000);
  });
  document.addEventListener('keydown', event => { if (event.key === 'Escape') reset(); });
  window.addEventListener('pageshow', reset);
  window.addEventListener('pagehide', reset);
})();
