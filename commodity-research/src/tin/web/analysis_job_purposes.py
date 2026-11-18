"""Task labels and the workbench views that can present each result."""
from dataclasses import dataclass


@dataclass(frozen=True)
class Purpose:
    title: str
    module: str
    path: str
    phases: dict[str, str]
    fragment: str = ''


DRAFT_PHASES = {'queued': '等待起草', 'selecting': '核对资料范围', 'analyzing': '正在起草',
                'validating': '正在核对草稿'}
SUGGESTION_PHASES = {'queued': '等待生成', 'selecting': '核对资料范围',
                     'loading_catalog': '读取指标目录', 'analyzing': '正在生成建议',
                     'validating': '正在核对候选'}
WORKBENCH_SETUP = 'workbench_setup'
PURPOSES = {
    'judgments': Purpose('研究逻辑分析', 'judgments', '/sn/judgment', {
        'queued': '等待分析', 'selecting': '核对资料范围', 'analyzing': '正在分析',
        'validating': '核对分析结果'}),
    'diagram': Purpose('产业图配置生成', 'diagram', '/sn/diagram', {
        'queued': '等待生成', 'reading_source': '读取原件', 'loading_catalog': '读取指标目录',
        'selecting': '匹配指标', 'generating': '生成图结构', 'validating': '核对配置'}),
    'monitor_draft': Purpose('监测草稿生成', 'home', '/', {
        **DRAFT_PHASES, 'loading_catalog': '读取指标目录', 'selecting': '正在匹配监测指标'}),
    'report_dimension_draft': Purpose('日报周报结论起草', 'reports', '/sn/reports', {
        **DRAFT_PHASES, 'reading_source': '正在读取报告事实与依据'}),
    'report_chart_import': Purpose('往期周报图表识别', 'indicators', '/sn/charts/import', {
        'queued': '等待识别', 'selecting': '核对资料范围', 'reading_source': '正在读取周报',
        'loading_catalog': '读取指标目录', 'analyzing': '正在识别图表', 'generating': '正在匹配图表指标',
        'validating': '正在核对图表候选'}, 'report-chart-import'),
    'dimension_inference': Purpose('周报研究维度归纳', 'indicators', '/sn/dimensions', {
        **SUGGESTION_PHASES, 'reading_source': '正在读取周报', 'analyzing': '正在归纳研究维度'}),
    'dimension_fields': Purpose('研究维度指标建议', 'indicators', '/sn/dimensions', {
        **SUGGESTION_PHASES, 'dimension_queries': '正在归纳指标检索方向', 'dimension_fields': '正在选择指标'}),
    'focus_suggestions': Purpose('核心关注建议生成', 'market', '/sn', SUGGESTION_PHASES),
    'judgment_dimensions': Purpose('研究判断维度补标', 'judgments', '/sn/judgment', {
        **SUGGESTION_PHASES, 'analyzing': '正在补标维度'}),
    'strategy_text': Purpose('策略原文整理', 'strategies', '/sn/strategies', {
        **DRAFT_PHASES, 'reading_source': '正在读取策略原文与依据', 'generating': '正在整理策略原文'}),
    'strategy_generation': Purpose('策略方案生成', 'strategies', '/sn/strategies', {
        **DRAFT_PHASES, 'analyzing': '正在生成策略方案', 'generating': '正在生成策略方案'}),
    WORKBENCH_SETUP: Purpose('从周报建立工作台', 'home', '/', {
        'queued': '等待建立', 'reading_source': '读取周报', 'dimensions': '归纳维度', 'indicators': '建议指标',
        'charts': '识别图表', 'layout': '整理周报版式', 'focus': '生成核心关注建议', 'diagram': '生成产业图骨架',
        'judgment': '生成研判草稿', 'strategy': '生成策略草稿', 'monitors': '生成监测候选'}),
}
DRAFT_PURPOSES = frozenset(PURPOSES) - {'judgments', 'diagram'}
TERMINAL_LABELS = {'succeeded': '已完成 · 待核对', 'failed': '未完成', 'interrupted': '已中断',
                   'expired': '已过期', 'applied': '已应用', 'superseded': '已有更新结果'}
