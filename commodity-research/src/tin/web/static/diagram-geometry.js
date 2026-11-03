/* Presentation geometry only: IDs, relations, bindings and business parents stay unchanged. */
(function (root) {
  const roles = new Set(['supply', 'target', 'demand', 'inventory', 'balance', 'macro', 'reference']);
  const GAP = 32, BAND = 64, COLUMN = 60, PAD = 18, HEAD = 28, MIN = 186;
  const INNER_PAD = 14, INNER_HEAD = 34, INNER_GAP = 16, INNER_COLUMN = 24, ROWS = 4, HEIGHT = 368;
  const box = node => node && ['x', 'y', 'w', 'h'].every(key => Number.isFinite(node[key])) && node.w > 0 && node.h > 0;
  const center = node => ({x: node.x + node.w / 2, y: node.y + node.h / 2});
  const overlap = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
  const bounds = nodes => ({x: Math.min(...nodes.map(n => n.x)), y: Math.min(...nodes.map(n => n.y)),
    w: Math.max(...nodes.map(n => n.x + n.w)) - Math.min(...nodes.map(n => n.x)),
    h: Math.max(...nodes.map(n => n.y + n.h)) - Math.min(...nodes.map(n => n.y))});
  function researchRoots(layout) {
    const nodes = layout.nodes || [], by = new Map(nodes.map(node => [node.id, node]));
    const declared = nodes.filter(node => node.role);
    if (!declared.length || declared.some(node => node.kind !== 'group' || node.parent || !roles.has(node.role))) return null;
    const roots = nodes.filter(node => !node.parent || !by.has(node.parent));
    if (roots.filter(node => node.role === 'target').length !== 1 || roots.some(node => !box(node))) return null;
    return roots;
  }
  function arrange(layout) {
    if (!researchRoots(layout)) return null;
    const result = {...layout, nodes: layout.nodes.map(node => ({...node}))};
    const roots = researchRoots(result), children = new Map();
    for (const node of result.nodes) {
      if (!children.has(node.parent)) children.set(node.parent, []);
      children.get(node.parent).push(node);
    }
    const buckets = Object.fromEntries([...roles].map(role => [role, roots.filter(node => (node.role || 'reference') === role)]));
    const x0 = Math.min(...roots.map(node => node.x)), y0 = Math.min(...roots.map(node => node.y));
    function move(node, x, y) {
      const dx = x - node.x, dy = y - node.y, pending = [node], seen = new Set();
      while (pending.length) {
        const current = pending.pop(); if (seen.has(current.id)) continue; seen.add(current.id);
        current.x += dx; current.y += dy; pending.push(...(children.get(current.id) || []));
      }
    }
    // Reflow is explicit. Parent relations remain authoritative, while a long
    // list can use the canvas width instead of making every ancestor taller.
    const fitted = new Set(), visiting = new Set();
    function pack(group) {
      if (fitted.has(group.id)) return true;
      if (visiting.has(group.id)) return false;
      visiting.add(group.id);
      const items = children.get(group.id) || [];
      for (const node of items) if (node.kind === 'group' && !pack(node)) return false;
      let x = group.x + INNER_PAD, y = group.y + INNER_HEAD, count = 0, columnW = 0, bottom = y;
      for (const node of items) {
        if (count && (count >= ROWS || y + node.h - group.y - INNER_HEAD > HEIGHT)) {
          x += columnW + INNER_COLUMN; y = group.y + INNER_HEAD; count = 0; columnW = 0;
        }
        move(node, x, y); columnW = Math.max(columnW, node.w);
        bottom = Math.max(bottom, y + node.h); y += node.h + INNER_GAP; count++;
      }
      if (items.length) { group.w = Math.max(MIN, x + columnW - group.x + INNER_PAD); group.h = bottom - group.y + INNER_PAD; }
      visiting.delete(group.id); fitted.add(group.id); return true;
    }
    for (const node of result.nodes) if (node.kind === 'group' && !pack(node)) return null;
    const width = list => list.reduce((sum, node) => sum + node.w, 0) + Math.max(0, list.length - 1) * GAP;
    const height = list => Math.max(0, ...list.map(node => node.h));
    const stock = [...buckets.inventory, ...buckets.balance], macro = buckets.macro;
    const slots = [buckets.supply, buckets.target, stock, buckets.demand].filter(list => list.length);
    const centerY = y0 + Math.max(...slots.map(list => height(list) / 2 + (list === stock ? HEAD : 0)));
    function row(list, x, cy) {
      for (const node of list) { move(node, x, cy - node.h / 2); x += node.w + GAP; }
    }
    let x = x0, targetX;
    for (const list of slots) {
      const slotW = list === buckets.target ? Math.max(width(list), width(macro)) : width(list) + (list === stock ? PAD * 2 : 0);
      row(list, x + (slotW - width(list)) / 2, centerY);
      if (list === buckets.target) targetX = x + slotW / 2;
      x += slotW + COLUMN;
    }
    const target = buckets.target[0];
    if (macro.length) row(macro, targetX - width(macro) / 2, target.y + target.h + BAND + height(macro) / 2);
    const placed = roots.filter(node => (node.role || 'reference') !== 'reference');
    const bottom = Math.max(...placed.map(node => node.y + node.h));
    row(buckets.reference, x0, bottom + BAND + height(buckets.reference) / 2);
    return result;
  }
  function regions(layout) {
    const roots = researchRoots(layout); if (!roots) return [];
    const inventory = roots.filter(node => node.role === 'inventory'), balance = roots.filter(node => node.role === 'balance');
    if (!inventory.length && !balance.length) return [];
    const members = [...inventory, ...balance], connected = new Set([members[0].id]);
    const nearby = (a, b) => {
      const dx = Math.max(0, a.x - b.x - b.w, b.x - a.x - a.w);
      const dy = Math.max(0, a.y - b.y - b.h, b.y - a.y - a.h);
      return dx <= BAND && dy <= BAND && (Math.min(a.y + a.h, b.y + b.h) > Math.max(a.y, b.y)
        || Math.min(a.x + a.w, b.x + b.w) > Math.max(a.x, b.x));
    };
    for (let pass = 0; pass < members.length; pass++) for (const a of members) if (connected.has(a.id)) {
      for (const b of members) if (nearby(a, b)) connected.add(b.id);
    }
    if (connected.size !== members.length) return [];
    const area = bounds(members), region = {x: area.x - PAD, y: area.y - HEAD, w: area.w + PAD * 2, h: area.h + HEAD + PAD};
    if (roots.some(node => !connected.has(node.id) && overlap(region, node))) return [];
    return [{...region, label: inventory.length && balance.length ? '库存与平衡' : inventory.length ? '库存' : '平衡', members: members.map(node => node.id)}];
  }
  function ancestors(node, by) {
    const result = new Set();
    while (node && node.parent && !result.has(node.parent)) { result.add(node.parent); node = by.get(node.parent); }
    return result;
  }
  function simplify(points) {
    const result = [];
    for (const point of points) {
      if (result.length && result.at(-1).x === point.x && result.at(-1).y === point.y) continue;
      while (result.length > 1) {
        const a = result.at(-2), b = result.at(-1);
        const straight = a.x === b.x && b.x === point.x || a.y === b.y && b.y === point.y;
        if (!straight || (b.x - a.x) * (point.x - b.x) < 0 || (b.y - a.y) * (point.y - b.y) < 0) break;
        result.pop();
      }
      result.push(point);
    }
    return result;
  }
  function segmentHit(a, b, rect) {
    const epsilon = 1e-6;
    return a.x === b.x ? a.x > rect.x + epsilon && a.x < rect.x + rect.w - epsilon
      && Math.max(Math.min(a.y, b.y), rect.y) < Math.min(Math.max(a.y, b.y), rect.y + rect.h) - epsilon
      : a.y > rect.y + epsilon && a.y < rect.y + rect.h - epsilon
        && Math.max(Math.min(a.x, b.x), rect.x) < Math.min(Math.max(a.x, b.x), rect.x + rect.w) - epsilon;
  }
  function pathGeometry(points, collisions) {
    const end = points.at(-1), previous = points.at(-2) || end;
    const length = Math.abs(end.x - previous.x) + Math.abs(end.y - previous.y) || 1;
    const cut = Math.min(12, length / 2), n = value => Number(value.toFixed(2));
    return {points, collisions,
      d: points.map((point, index) => `${index ? 'L' : 'M'} ${n(point.x)} ${n(point.y)}`).join(' '),
      kx: end.x - (end.x - previous.x) / length * cut, ky: end.y - (end.y - previous.y) / length * cut};
  }
  function route(layout, a, b) {
    if (!box(a) || !box(b) || a.id === b.id) return null;
    const nodes = (layout.nodes || []).filter(box), by = new Map(nodes.map(node => [node.id, node]));
    const aParents = ancestors(a, by), bParents = ancestors(b, by), containers = new Set([...aParents, ...bParents]);
    const obstacles = nodes.filter(node => !containers.has(node.id)).map(node => {
      const pad = node.id === a.id || node.id === b.id ? 0 : 6;
      return {x: node.x - pad, y: node.y - pad, w: node.w + pad * 2, h: node.h + pad * 2};
    });
    // Most neighbouring group/leaf relations share a clear horizontal or
    // vertical corridor. Avoid route-search work during every drag frame.
    const direct = [], ac = center(a), bc = center(b);
    const top = Math.max(a.y, b.y), bottom = Math.min(a.y + a.h, b.y + b.h);
    if (top <= bottom && (a.x + a.w <= b.x || b.x + b.w <= a.x)) {
      const y = Math.max(top, Math.min(bottom, (ac.y + bc.y) / 2)), right = a.x < b.x;
      direct.push([{x: right ? a.x + a.w : a.x, y}, {x: right ? b.x : b.x + b.w, y}]);
    }
    const left = Math.max(a.x, b.x), right = Math.min(a.x + a.w, b.x + b.w);
    if (left <= right && (a.y + a.h <= b.y || b.y + b.h <= a.y)) {
      const x = Math.max(left, Math.min(right, (ac.x + bc.x) / 2)), down = a.y < b.y;
      direct.push([{x, y: down ? a.y + a.h : a.y}, {x, y: down ? b.y : b.y + b.h}]);
    }
    for (const points of direct) if (obstacles.every(rect => !segmentHit(points[0], points[1], rect))) return pathGeometry(points, 0);
    const ports = (node, inward) => {
      const c = center(node), sign = inward ? -1 : 1;
      return [[node.x + node.w, c.y, 1, 0], [node.x, c.y, -1, 0], [c.x, node.y, 0, -1], [c.x, node.y + node.h, 0, 1]]
        .map(([x, y, dx, dy]) => ({point: {x, y}, stub: {x: x + dx * 10 * sign, y: y + dy * 10 * sign}}));
    };
    const all = bounds(nodes.length ? nodes : [a, b]);
    const corridors = (axis, midpoint) => {
      const size = axis === 'x' ? 'w' : 'h';
      const candidates = nodes.flatMap(node => [node[axis] - 10, node[axis] + node[size] + 10]);
      return [...new Set([midpoint, all[axis] - 20, all[axis] + all[size] + 20,
        ...candidates.sort((x, y) => Math.abs(x - midpoint) - Math.abs(y - midpoint)).slice(0, 14)])];
    };
    const xs = corridors('x', (ac.x + bc.x) / 2), ys = corridors('y', (ac.y + bc.y) / 2);
    let best;
    for (const p of ports(a, bParents.has(a.id))) for (const q of ports(b, aParents.has(b.id))) {
      const s = p.stub, t = q.stub;
      const middle = [[s, {x: t.x, y: s.y}, t], [s, {x: s.x, y: t.y}, t],
        ...xs.map(x => [s, {x, y: s.y}, {x, y: t.y}, t]),
        ...ys.map(y => [s, {x: s.x, y}, {x: t.x, y}, t])];
      for (const chain of middle) {
        const points = simplify([p.point, ...chain, q.point]);
        let collisions = 0, length = 0;
        for (let index = 1; index < points.length; index++) {
          const prev = points[index - 1], point = points[index];
          length += Math.abs(point.x - prev.x) + Math.abs(point.y - prev.y);
          for (const obstacle of obstacles) if (segmentHit(prev, point, obstacle)) collisions++;
        }
        const score = collisions * 1e9 + length + Math.max(0, points.length - 2) * 24;
        if (!best || score < best.score) best = {points, score, collisions};
      }
    }
    return pathGeometry(best.points, best.collisions);
  }
  const api = {arrange, regions, route, canArrange: layout => Boolean(researchRoots(layout))};
  root.DiagramGeometry = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
