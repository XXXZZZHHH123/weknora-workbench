import palette from './chartPalette.generated.json';

// Ship a checked mirror so the frontend Docker build stays self-contained.
export const CHART_SERIES_COLORS: readonly string[] = Object.freeze(palette.colors);
