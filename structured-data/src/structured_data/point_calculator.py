"""Bounded decimal arithmetic over source-bound variables. No eval or SQL."""
import ast
import re
from decimal import Decimal, DecimalException, ROUND_HALF_UP, localcontext


def additive_statistics_unit(expression, fields):
    """Addition/subtraction preserves actual source units, even if not stated."""
    units={f['role']:('条' if f.get('operation')=='count' else (f['candidates'][0].get('unit') or ('unknown',f['candidates'][0]['series_id'])))
           for f in fields if len(f.get('candidates',[]))==1}
    try:
        tree=ast.parse(expression,mode='eval')
        names={n.id for n in ast.walk(tree) if isinstance(n,ast.Name)}
        if not names or names-set(units):return None
        if any(not isinstance(n,(ast.Expression,ast.BinOp,ast.UnaryOp,ast.Name,ast.Load,ast.Add,ast.Sub,ast.UAdd,ast.USub)) for n in ast.walk(tree)):return None
        bases={units[name] for name in names}
        if len(bases)!=1:raise ValueError('相加减的统计值必须具有相同来源单位，不能将不同单位直接相减')
        basis=next(iter(bases))
        return '原表未注明单位' if isinstance(basis,tuple) else basis
    except (SyntaxError,TypeError):return None


def validate_output_unit(output_unit, sources, question=''):
    """Do not invent a per-unit basis absent from both source metadata and the user."""
    import re
    if re.search(r'[,，;；]',re.split(r'[（(]',str(output_unit),maxsplit=1)[0]):
        raise ValueError('计算结果只能有一个目标单位；各来源原值会自动展示，不要将原币与目标币单位并列')
    parts=re.split(r'[/／]',str(output_unit),maxsplit=1)
    if len(parts)<2:return
    denominator=re.split(r'[（(，,;；\s]',parts[1],maxsplit=1)[0].strip()
    if denominator and not any(denominator in str(s.get('unit') or '') for s in sources) and denominator not in question:
        raise ValueError(f'结果计量基准“{denominator}”没有来源单位或用户说明的依据；仅换币时保持原计量基准，输出目标币种并注明原计量单位未注明，不得擅自添加每吨等单位')


def validate_constants(expression, sources, question='', output_unit=''):
    """Require provenance for nontrivial numeric literals in model-generated formulas.

    These are measurement definitions, not mappings from business names to columns.
    Source units come from the imported catalogue, never model explanations.
    """
    tree=ast.parse(expression,mode='eval')
    allowed={Decimal('0'),Decimal('1')}
    for number in re.findall(r'(?<![\w.])\d+(?:\.\d+)?(?![\w.])',question or ''):
        allowed.add(Decimal(number))
    source_units=' '.join(s.get('unit') or '' for s in sources)
    units=source_units+' '+(output_unit or '')
    if '美分' in source_units or 'cent' in source_units.lower():
        allowed.update((Decimal('0.01'),Decimal('100')))
    if ('磅' in units or 'lb' in units.lower()) and any(x in units for x in ('千克','公斤','kg','吨')):
        allowed.add(Decimal('0.45359237'))
    if '吨' in units and any(x in units for x in ('千克','公斤','kg','磅','lb')):
        allowed.add(Decimal('1000'))
    if any(x in output_unit for x in ('%','百分')):
        allowed.add(Decimal('100'))
    for node in ast.walk(tree):
        if isinstance(node,ast.Constant) and type(node.value) in (int,float):
            value=Decimal(ast.get_source_segment(expression,node))
            if value not in allowed:
                raise ValueError(f'计算常数{value}没有原始单位或用户要求的依据。请按原始单位重新规划，不能额外缩放数值')


def calculate(expression,values):
    if not isinstance(expression,str) or len(expression)>400:raise ValueError('计算式过长')
    try:
        tree=ast.parse(expression,mode='eval')
    except SyntaxError:raise ValueError('计算式无效') from None
    if len(list(ast.walk(tree)))>80:raise ValueError('计算式过于复杂')
    used=set()
    def visit(node):
        if isinstance(node,ast.Name) and node.id in values:
            used.add(node.id);return Decimal(str(values[node.id]))
        if isinstance(node,ast.Constant) and type(node.value) in (int,float):
            literal=ast.get_source_segment(expression,node)
            value=Decimal(literal)
            if not value.is_finite() or abs(value)>Decimal('1e12'):raise ValueError('计算常数超出允许范围')
            return value
        if isinstance(node,ast.UnaryOp) and isinstance(node.op,(ast.UAdd,ast.USub)):
            value=visit(node.operand);return -value if isinstance(node.op,ast.USub) else value
        if isinstance(node,ast.BinOp) and isinstance(node.op,(ast.Add,ast.Sub,ast.Mult,ast.Div)):
            a,b=visit(node.left),visit(node.right)
            if isinstance(node.op,ast.Add):return a+b
            if isinstance(node.op,ast.Sub):return a-b
            if isinstance(node.op,ast.Mult):return a*b
            return a/b
        raise ValueError('只允许已确认字段变量、数字及加减乘除，不允许代码、函数或外部访问')
    try:
        with localcontext() as ctx:
            ctx.prec=34
            value=visit(tree.body)
            if used!=set(values):raise ValueError('计算式必须使用全部已确认字段，且不能添加未知字段')
            if not value.is_finite() or abs(value)>Decimal('1e24'):raise ValueError('计算结果超出允许范围')
            return {'value':float(value),'value_decimal':str(value),
                    'rounded_value':str(value.quantize(Decimal('0.01'),rounding=ROUND_HALF_UP)),
                    'expression':expression}
    except (DecimalException,ZeroDivisionError):raise ValueError('计算失败：除数为零或数值无效') from None
