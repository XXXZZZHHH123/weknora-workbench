import { ExcelPreviewArchive } from './excelPreview';

let workbook: ExcelPreviewArchive | null = null;
let operations = Promise.resolve();
let latestChartRequest = 0;
async function handle(event: MessageEvent) {
  const { id, type, data, extension, sheet, row, column, chartId } = event.data;
  try {
    if (type === 'cancel-charts') return;
    if (type === 'chart' && id !== latestChartRequest) { self.postMessage({ id, error: '图表已切换，旧请求已取消。' }); return; }
    if (type === 'open') {
      workbook = null;
      workbook = await ExcelPreviewArchive.open(data, extension);
      self.postMessage({ id, value: workbook.sheetNames });
    } else if (type === 'page' && workbook) {
      self.postMessage({ id, value: await workbook.page(sheet, row, column) });
    } else if (type === 'chart-list' && workbook) {
      self.postMessage({ id, value: await workbook.chartList(sheet) });
    } else if (type === 'chart' && workbook) {
      self.postMessage({ id, value: await workbook.chart(sheet, chartId) });
    } else throw new Error('预览已关闭，请重新打开。');
  } catch (error) {
    self.postMessage({ id, error: error instanceof Error ? error.message : '无法解析此工作表。' });
  }
}
// Page changes must release the previous parsed model before starting another
// one, even when several navigation messages arrive in the same event loop.
self.onmessage = (event: MessageEvent) => {
  if (event.data.type === 'chart') latestChartRequest = event.data.id;
  if (event.data.type === 'cancel-charts' || event.data.type === 'open') latestChartRequest = 0;
  operations = operations.then(() => handle(event));
};
