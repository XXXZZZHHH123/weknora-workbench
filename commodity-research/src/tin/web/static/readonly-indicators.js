document.addEventListener('DOMContentLoaded', () => {
  const root = document.getElementById('indicator-workspace');
  if (root?.dataset.readonly !== 'true') return;
  const get = id => document.getElementById(`indicator-${id}`);
  const rows = [...root.querySelectorAll('[data-indicator-row]')];
  const groups = [...root.querySelectorAll('[data-indicator-group]')];
  const search = get('search');
  const normalize = value => String(value || '').normalize('NFKC').toLocaleLowerCase();
  const text = new Map(rows.map(row => [row, normalize(row.dataset.search)]));
  function filter() {
    const terms = normalize(search.value).trim().split(/\s+/).filter(Boolean);
    let total = 0;
    rows.forEach(row => { row.hidden = !terms.every(term => text.get(row).includes(term)); if (!row.hidden) total++; });
    groups.forEach((group, index) => {
      const count = [...group.querySelectorAll('[data-indicator-row]')].filter(row => !row.hidden).length;
      group.hidden = !count;
      group.querySelector('[data-group-count]').textContent = count;
      if (terms.length) group.open = true;
      const jump = root.querySelector(`[data-target="indicator-group-${index + 1}"]`);
      if (jump) { jump.hidden = !count; jump.querySelector('[data-jump-count]').textContent = count; }
    });
    get('result-count').textContent = `${total} / ${rows.length} 项`;
    get('no-results').hidden = total > 0 || !rows.length;
    get('search-clear-input').hidden = !search.value.trim();
    const jump = root.querySelector('[data-jump-total]'); if (jump) jump.textContent = total;
  }
  const clear = () => { search.value = ''; filter(); search.focus(); };
  search.addEventListener('input', filter);
  get('search-clear-input').addEventListener('click', clear);
  get('search-clear').addEventListener('click', clear);
  get('expand').addEventListener('click', () => groups.filter(group => !group.hidden).forEach(group => { group.open = true; }));
  get('collapse').addEventListener('click', () => groups.forEach(group => { group.open = false; }));
  filter();
});
