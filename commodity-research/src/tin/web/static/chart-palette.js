/* One ordered chart palette; semantic up/down and explicit custom colors remain independent. */
(function (root) {
  const supplied = typeof module !== 'undefined' && module.exports ? require('./chart-palette.json') : root.ChartPaletteColors;
  const colors = Object.freeze(Array.isArray(supplied) ? supplied.slice() : []);
  function color(index = 0, custom) {
    if (typeof custom === 'string' && custom.trim() && !/["'<>;{}]/.test(custom)) return custom.trim();
    const position = Number.isFinite(index) ? Math.max(0, Math.trunc(index)) : 0;
    return colors.length ? colors[position % colors.length] : `var(--chart-series-${position + 1})`;
  }
  const api = Object.freeze({colors, color});
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.ChartPalette = api;
  const style = root.document?.documentElement?.style;
  colors.forEach((value, index) => style?.setProperty('--chart-series-' + (index + 1), value));
})(typeof window !== 'undefined' ? window : globalThis);
