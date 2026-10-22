import { ExcelPreviewArchive } from './excelPreview';

let workbook: ExcelPreviewArchive | null = null;
let operations = Promise.resolve();
async function handle(event: MessageEvent) {
  const { id, type, data, extension, sheet, row, column } = event.data;
  try {
    if (type === 'open') {
      workbook = null;
      workbook = await ExcelPreviewArchive.open(data, extension);
      self.postMessage({ id, value: workbook.sheetNames });
    } else if (type === 'page' && workbook) {
      self.postMessage({ id, value: await workbook.page(sheet, row, column) });
    } else throw new Error('预览已关闭，请重新打开。');
  } catch (error) {
    self.postMessage({ id, error: error instanceof Error ? error.message : '无法解析此工作表。' });
  }
}
// Page changes must release the previous parsed model before starting another
// one, even when several navigation messages arrive in the same event loop.
self.onmessage = (event: MessageEvent) => { operations = operations.then(() => handle(event)); };
