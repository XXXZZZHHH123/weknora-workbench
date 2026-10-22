import { ExcelPreviewWorkbook, validateExcelInflation } from './excelPreview';

let workbook: ExcelPreviewWorkbook | null = null;
self.onmessage = async (event: MessageEvent) => {
  const { id, type, data, extension, sheet, row, column } = event.data;
  try {
    if (type === 'open') {
      workbook = null;
      await validateExcelInflation(data);
      workbook = new ExcelPreviewWorkbook(data, extension);
      self.postMessage({ id, value: workbook.sheetNames });
    } else if (type === 'page' && workbook) {
      self.postMessage({ id, value: workbook.page(sheet, row, column) });
    } else throw new Error('预览已关闭，请重新打开。');
  } catch (error) {
    self.postMessage({ id, error: error instanceof Error ? error.message : '无法解析此工作表。' });
  }
};
