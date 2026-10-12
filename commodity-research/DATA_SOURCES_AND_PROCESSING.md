# 商品研究的数据来源与处理

按实际 `tin.ingest.runner.FETCHERS` 和导入代码整理，替代旧文件中来自 research-workbench 的不适用列表。描述接入范围，不保证外部接口随时可用。

| 数据 | 接入方式 | 实现 |
|---|---|---|
| 上期所具体沪锡合约行情 | 官方行情日报 | ingest/shfe.py |
| 注册仓单、交易所周库存 | 官方日/周报 | ingest/shfe.py |
| 人民币中间价 | 外汇交易中心当日接口 | ingest/cfets.py |
| VIX | CBOE 官方 CSV | ingest/cboe.py |
| 利率、美元、信用、流动性及宏观周期序列 | FRED CSV，列表见 FRED_SERIES | ingest/fred.py |
| 供应商终端工作簿 | 人工取得、预览确认后导入 | ingest/vendor_terminal.py |
| 人工值与 Excel/CSV | 明确口径、录入人和来源 | ingest/record.py、excel_importer.py |
| 内网无法直连的原件 | 联网下载、离线回放 | ingest/raw.py |

观测经过 record 校验：登记、口径一致、人工来源、重复值跳过、修订追加。失败记 fetch_runs，不补 NULL 或零。基差、跨期价差、仓单变化由 compute 计算；时点或口径不匹配时阻断。

研究持续观测与 structured-data 原 Excel 快照分别维护。首版聊天可读研究库；通用 Excel 字段到研究指标和原单元格的桥接尚未实现。
