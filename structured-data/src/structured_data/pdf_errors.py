"""Safe, actionable processing failures; never expose provider exception text."""

class PDFProcessingError(ValueError):
    def __init__(self, code, message, **metrics):
        super().__init__(message)
        self.public_code = code
        self.metrics = metrics


_PROBLEMS = {
    'MODEL_NOT_CONFIGURED': ('尚未选择表格处理模型', '请先选择一个可用的通用模型，再继续整理。', 'model_settings', False),
    'MODEL_CONFIG_INVALID': ('表格处理模型配置无效', '请重新选择可用模型，再继续整理。', 'model_settings', False),
    'MODEL_AUTH': ('模型登录或调用权限已失效', '请重新登录或检查所选模型的访问权限，再继续整理。', 'model_settings', False),
    'MODEL_NOT_FOUND': ('所选模型已不可用', '请选择当前空间可用的通用模型，再继续整理。', 'model_settings', False),
    'MODEL_RATE_LIMIT': ('模型调用暂时达到限制', '请稍后重试，已入库的表格不会重复处理。', 'retry', True),
    'MODEL_TIMEOUT': ('等待模型响应超时', '请稍后重试，原件和已完成结果均已保留。', 'retry', True),
    'MODEL_CONNECTION': ('暂时无法连接模型服务', '请稍后重试；持续失败时请检查模型服务连接。', 'retry', True),
    'MODEL_HTTP_ERROR': ('模型服务暂时未能完成请求', '请稍后重试；持续失败时请联系管理员检查模型服务。', 'retry', True),
    'MODEL_OUTPUT_INVALID': ('模型返回的整理结果格式无效', '可重新整理；系统没有采用无效的结果。', 'retry', True),
    'MODEL_OUTPUT_TRUNCATED': ('模型返回的整理结果不完整', '可重新整理，或选择输出能力更大的模型；系统没有采用截断结果。', 'retry', True),
    'MODEL_RESPONSE_LIMIT': ('模型返回内容超过处理范围', '请更换模型或减少一次处理的表格范围。', 'model_settings', False),
    'MODEL_INPUT_LIMIT': ('整理内容超过模型单次请求范围', '请减少一次处理的表格范围，反复重试同一内容不会解决此限制。', 'review', False),
    'GROUPING_INVALID': ('模型未能完整划分表格', '分组存在遗漏、重复或缺少依据，可重新整理。未发布不完整结果。', 'retry', True),
    'GROUPING_INPUT_LIMIT': ('文件中的表格信息超过单次整理范围', '请拆分文件后上传，或在查看表格中选择较少片段处理。反复重试同一文件不会解决此限制。', 'review', False),
    'PLAN_INPUT_LIMIT': ('这组表格超过单次字段识别范围', '请在查看表格中选择较少片段处理，原件没有被截断。', 'review', False),
    'SOURCE_INVALID': ('原件暂不可用或内容已改变', '请检查原文件，必要时重新上传；系统没有采用改变后的来源。', 'upload', False),
    'PLAN_INVALID': ('表格字段或行列映射未通过检查', '请查看对应表格的表头、数值和续表关系，或重新识别。', 'review', False),
    'EXTRACTION_REVIEW_REQUIRED': ('表格存在识别疑点', '请查看标出的表格并核对原件；其余可用表格会继续入库。', 'review', False),
    'PROCESSING_FAILED': ('自动整理遇到处理错误', '原件和已完成结果已保留。可重试；持续失败时请提供下方错误代码。', 'retry', True),
}


def processing_problem(exc, *, stage):
    code = getattr(exc, 'public_code', None)
    if code not in _PROBLEMS:
        if stage == 'source':
            code = 'SOURCE_INVALID'
        elif stage == 'validation' and isinstance(exc, ValueError):
            code = 'PLAN_INVALID'
        else:
            code = 'PROCESSING_FAILED'
    title, message, action, retryable = _PROBLEMS[code]
    result = dict(code=code, stage=stage, title=title, message=message,
                  action=action, retryable=retryable)
    metrics = getattr(exc, 'metrics', {})
    result.update({k: v for k, v in metrics.items()
                   if k in {'input_chars', 'limit_chars', 'input_bytes', 'limit_bytes'} and type(v) is int and v >= 0})
    return result
