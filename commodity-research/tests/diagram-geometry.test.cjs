const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const base = path.join(__dirname, '../src/tin/web/static');

function harness(layout) {
  const context = vm.createContext({window: {}, document: {getElementById: () => null, addEventListener() {}},
    URLSearchParams, location: {search: ''}, console});
  const geometry = path.join(base, 'diagram-geometry.js');
  if (fs.existsSync(geometry)) vm.runInContext(fs.readFileSync(geometry, 'utf8'), context);
  vm.runInContext(fs.readFileSync(path.join(base, 'diagram.js'), 'utf8') + `
    dgRender = () => {}; dgFit = () => {}; dgLoad = () => {}; dgBindCanvas = () => {};
    dgSyncSave = () => {}; dgSyncEditUI = () => {}; dgSyncCompareBtn = () => {}; showToast = () => {};
    this.view = {state: dg, tidy: dgTidyGroups, init: dgInit, route: dgEdgeGeom};`, context);
  context.view.state.layout = structuredClone(layout);
  return {view: context.view, window: context.window};
}

function fixture() {
  const roles = ['demand', 'inventory', 'target', 'macro', 'balance', 'supply'];
  const nodes = roles.map((role, index) => ({id: 'root-' + index, kind: 'group', role,
    label: 'Repeated heading', x: 50, y: 50 + index * 200, w: 210, h: 140,
    binding: {kind: 'none'}}));
  for (const group of nodes.slice()) nodes.push({id: 'leaf-' + group.id, label: 'Same measurement',
    parent: group.id, x: group.x + 16, y: group.y + 32, w: 170, h: 80,
    binding: {kind: 'series', series_id: 'synthetic-' + group.role}});
  return {nodes, edges: [{from: 'root-5', to: 'root-2'}, {from: 'root-2', to: 'root-0'},
    {from: 'root-3', to: 'root-2', kind: 'influence'},
    {from: 'leaf-root-5', to: 'leaf-root-0', kind: 'influence'}]};
}
const center = node => ({x: node.x + node.w / 2, y: node.y + node.h / 2});
const overlaps = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

test('explicit research reordering keeps supply-target-demand horizontal, stock together above and macro below', () => {
  const original = fixture(), {view} = harness(original);
  view.tidy();
  const groups = Object.fromEntries(view.state.layout.nodes.filter(node => node.role).map(node => [node.role, node]));
  assert(center(groups.supply).x < center(groups.target).x && center(groups.target).x < center(groups.demand).x);
  assert.equal(center(groups.supply).y, center(groups.target).y);
  assert.equal(center(groups.target).y, center(groups.demand).y);
  assert(groups.inventory.y + groups.inventory.h < groups.target.y);
  assert(groups.balance.y + groups.balance.h < groups.target.y);
  assert.equal(groups.inventory.y, groups.balance.y);
  assert(groups.macro.y > groups.target.y + groups.target.h);
  assert.equal(center(groups.macro).x, center(groups.target).x);
  for (const a of Object.values(groups)) for (const b of Object.values(groups)) if (a !== b) assert(!overlaps(a, b));
  assert.deepEqual(JSON.parse(JSON.stringify(view.state.layout.edges)), original.edges);
  for (const before of original.nodes) {
    const after = view.state.layout.nodes.find(node => node.id === before.id);
    assert.equal(after.parent, before.parent); assert.equal(after.role, before.role);
    assert.deepEqual(JSON.parse(JSON.stringify(after.binding)), before.binding);
  }
});

test('opening a saved manual layout never reorders its persisted coordinates', () => {
  const original = fixture(), {view, window} = harness(original);
  window.DG_TEMPLATE = {id: 41, name: 'Saved manual positions', layout: structuredClone(original)};
  view.init();
  assert.deepEqual(JSON.parse(JSON.stringify(view.state.layout.nodes)), original.nodes);
});

function sampledPath(geometry) {
  if (geometry.points) return geometry.points;
  const values = geometry.d.match(/-?\d+(?:\.\d+)?/g).map(Number);
  if (!geometry.d.includes('C')) return Array.from({length: values.length / 2}, (_, i) => ({x: values[i * 2], y: values[i * 2 + 1]}));
  const [x0, y0, x1, y1, x2, y2, x3, y3] = values;
  return Array.from({length: 101}, (_, index) => {
    const t = index / 100, u = 1 - t;
    return {x: u ** 3 * x0 + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t ** 3 * x3,
      y: u ** 3 * y0 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t ** 3 * y3};
  });
}
function samples(points) {
  return points.flatMap((point, index) => index ? Array.from({length: 51}, (_, at) => ({
    x: points[index - 1].x + (point.x - points[index - 1].x) * at / 50,
    y: points[index - 1].y + (point.y - points[index - 1].y) * at / 50})) : [point]);
}
test('routing bypasses unrelated group boxes while preserving the actual source and destination borders', () => {
  const a = {id: 'a', kind: 'group', x: 0, y: 60, w: 160, h: 100};
  const b = {id: 'b', kind: 'group', x: 500, y: 60, w: 160, h: 100};
  const blocker = {id: 'obstacle', kind: 'group', x: 240, y: 40, w: 160, h: 160};
  const original = {nodes: [a, b, blocker], edges: [{from: 'a', to: 'b', kind: 'influence'}]};
  const {view} = harness(original), route = view.route(a, b), points = sampledPath(route);
  assert(samples(points).every(point => !(point.x > blocker.x && point.x < blocker.x + blocker.w
    && point.y > blocker.y && point.y < blocker.y + blocker.h)), 'The relation must not cut through an unrelated group');
  const onBorder = (point, box) => point.x >= box.x && point.x <= box.x + box.w && point.y >= box.y && point.y <= box.y + box.h
    && [point.x === box.x, point.x === box.x + box.w, point.y === box.y, point.y === box.y + box.h].some(Boolean);
  assert(onBorder(points[0], a)); assert(onBorder(points.at(-1), b));
  assert.deepEqual(JSON.parse(JSON.stringify(view.state.layout)), original);
});

test('role layout moves nested subtrees together without introducing a stock/balance business group', () => {
  const geometry = require(path.join(base, 'diagram-geometry.js')), layout = fixture();
  const supply = layout.nodes.find(node => node.role === 'supply');
  layout.nodes.push({id: 'nested', kind: 'group', parent: supply.id, label: 'Repeated heading',
    x: supply.x + 10, y: supply.y + 20, w: 190, h: 115, binding: {kind: 'none'}});
  const leaf = layout.nodes.find(node => node.parent === supply.id); leaf.parent = 'nested';
  const original = structuredClone(layout), arranged = geometry.arrange(layout);
  const delta = id => {
    const before = original.nodes.find(node => node.id === id), after = arranged.nodes.find(node => node.id === id);
    return {x: after.x - before.x, y: after.y - before.y};
  };
  assert.deepEqual(delta(supply.id), delta('nested')); assert.deepEqual(delta('nested'), delta(leaf.id));
  assert.equal(arranged.nodes.length, original.nodes.length);
  assert.deepEqual(layout, original); assert.deepEqual(arranged.edges, original.edges);
  const regions = geometry.regions(arranged); assert.equal(regions.length, 1);
  assert.equal(regions[0].label, '库存与平衡');
  assert(regions[0].members.every(id => arranged.nodes.some(node => node.id === id && !node.parent)));
});

test('manual distant or obstructed inventory/balance groups do not get a giant shared frame', () => {
  const geometry = require(path.join(base, 'diagram-geometry.js'));
  const layout = geometry.arrange(fixture());
  const balance = layout.nodes.find(node => node.role === 'balance');
  balance.x += 3000;
  assert.equal(geometry.regions(layout).length, 0);
  const nearby = geometry.arrange(fixture()), region = geometry.regions(nearby)[0];
  nearby.nodes.push({id: 'unrelated', label: 'Independent note', x: region.x + 4, y: region.y + 4, w: 40, h: 30});
  assert.equal(geometry.regions(nearby).length, 0);
});

test('legacy names and invalid semantic roles never trigger semantic reordering', () => {
  const geometry = require(path.join(base, 'diagram-geometry.js')), layout = fixture();
  layout.nodes.forEach(node => { if (node.role) { node.label = node.role; delete node.role; } });
  assert.equal(geometry.arrange(layout), null); assert.deepEqual(geometry.regions(layout), []);
  const nested = fixture(); nested.nodes.find(node => node.role === 'macro').parent = 'root-2';
  assert.equal(geometry.arrange(nested), null);
});

test('the main role relations use straight group borders and macro influence keeps its actual arrow direction', () => {
  const geometry = require(path.join(base, 'diagram-geometry.js')), arranged = geometry.arrange(fixture());
  const by = Object.fromEntries(arranged.nodes.map(node => [node.id, node]));
  for (const edge of arranged.edges.slice(0, 3)) {
    const route = geometry.route(arranged, by[edge.from], by[edge.to]);
    assert.equal(route.collisions, 0); assert.equal(route.points.length, 2);
  }
  const influence = arranged.edges[2], route = geometry.route(arranged, by[influence.from], by[influence.to]);
  assert(route.points[0].y > route.points[1].y, 'Existing macro-to-target influence points upward after layout');
  assert.equal(influence.kind, 'influence');
  assert.deepEqual(geometry.arrange({...fixture(), edges: []}).edges, [], 'Geometry never invents missing material or influence relations');
});

test('tall supply and demand groups do not stretch the central stock-target-macro spacing', () => {
  const geometry = require(path.join(base, 'diagram-geometry.js')), layout = fixture();
  layout.nodes.find(node => node.role === 'supply').h = 1200;
  layout.nodes.find(node => node.role === 'demand').h = 920;
  const arranged = geometry.arrange(layout), groups = Object.fromEntries(arranged.nodes.filter(node => node.role).map(node => [node.role, node]));
  const region = geometry.regions(arranged)[0];
  assert.equal(groups.target.y - region.y - region.h, 64);
  assert.equal(groups.macro.y - groups.target.y - groups.target.h, 64);
  assert.equal(center(groups.supply).y, center(groups.target).y);
  assert.equal(center(groups.demand).y, center(groups.target).y);
  assert(Math.min(...arranged.nodes.map(node => node.y)) >= 0);
  for (const a of Object.values(groups)) for (const b of Object.values(groups)) if (a !== b) assert(!overlaps(a, b));
});
