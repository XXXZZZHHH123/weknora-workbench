(() => {
  'use strict';

  function element(tag, text) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function orderRows(host, rows, update) {
    host.replaceChildren();
    rows.forEach((dimension, index) => {
      const item = element('li', dimension.label + ' ');
      for (const [label, offset] of [['上移', -1], ['下移', 1]]) {
        const button = element('button', label); button.type = 'button'; button.className = 'report-button';
        button.disabled = index + offset < 0 || index + offset >= rows.length;
        button.setAttribute('aria-label', label + dimension.label);
        button.addEventListener('click', () => {
          [rows[index], rows[index + offset]] = [rows[index + offset], rows[index]];
          update(); orderRows(host, rows, update);
          const buttons = host.children[index + offset].querySelectorAll('button:not(:disabled)');
          (Array.from(buttons).find(control => control.textContent === label) || buttons[0])?.focus();
        });
        item.append(button);
      }
      host.append(item);
    });
  }

  window.ReportLayoutEditor = {
    render(host, draft, dimensions, options, proposal) {
      host.replaceChildren();
      host.hidden = !draft.layout && !proposal;
      if (host.hidden) return;
      host.open = Boolean(proposal);
      host.append(element('summary', '一页周报版式'));
      const intro = element('p', '核对维度顺序和证据图分组。分组只应用到本报告已选或已引用的图。');
      intro.className = 'report-muted'; host.append(intro);
      const byId = new Map(dimensions.map(row => [row.id, row]));
      const proposed = proposal?.dimensions || [];
      const order = proposal ? proposed.map(row => row.id) : draft.layout.dimension_order;
      const rows = [...new Set([...order, ...byId.keys()])].filter(id => byId.has(id)).map(id => byId.get(id));
      const pending = proposed.filter(row => !byId.has(row.id));
      const pendingIds = new Set(pending.map(row => row.id));
      if (pending.length) host.append(element('p', '待先保存研究维度：' + pending.map(row => row.label).join('、') + '。保存后可从核对清单重新打开版式。'));
      if (proposal?.notice) host.append(element('p', proposal.notice));
      const groupRows = new Map();
      for (const [id, group] of Object.entries(draft.layout?.figure_groups || {})) {
        if (byId.has(group)) groupRows.set(id, {group, title: options.get(id)?.name || id});
      }
      const unmapped = [];
      for (const group of [...(proposal?.groups || [])].reverse()) {
        for (const figure of group.figures) {
          if (!figure.series_id) { unmapped.push(figure.title); continue; }
          groupRows.set(figure.series_id, {group: group.id, label: group.label,
            title: figure.title || options.get(figure.series_id)?.name || figure.series_id});
        }
      }
      const keptGroups = [...groupRows.entries()].slice(0, 100);
      const update = () => {
        const figureGroups = Object.fromEntries(keptGroups.filter(([, row]) => byId.has(row.group)).map(([id, row]) => [id, row.group]));
        draft.layout = rows.length ? {dimension_order: rows.map(row => row.id), figure_groups: figureGroups} : null;
      };
      const list = element('ol'); list.setAttribute('aria-label', '维度顺序');
      host.append(element('h4', '维度顺序'), list); orderRows(list, rows, update);
      host.append(element('h4', '证据图分组'));
      if (!keptGroups.length) host.append(element('p', '没有可预填的证据图分组。'));
      if (unmapped.length) {
        const list = element('details'); list.append(element('summary', `待补图表映射 ${unmapped.length} 张`));
        const titles = element('ul'); unmapped.forEach(title => titles.append(element('li', title)));
        list.append(titles); host.append(list);
      }
      for (const [, row] of keptGroups) {
        const field = element('label', row.title + ' '), select = element('select');
        select.setAttribute('aria-label', row.title + '的证据图分组');
        select.append(new Option('按现有绑定分组', ''));
        if (pendingIds.has(row.group)) select.append(new Option(row.label + '（待保存）', row.group));
        for (const dimension of rows) select.append(new Option(dimension.label, dimension.id));
        select.value = byId.has(row.group) || pendingIds.has(row.group) ? row.group : '';
        select.addEventListener('change', () => { row.group = select.value; update(); });
        field.className = 'report-figure-option'; field.append(select); host.append(field);
      }
      update();
    },
  };
})();
