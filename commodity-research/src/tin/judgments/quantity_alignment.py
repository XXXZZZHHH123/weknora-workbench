"""Exact arithmetic and presentation for an explicitly confirmed rule scale."""
from decimal import Decimal, localcontext


MIN_SCALE_EXPONENT = -4
MAX_SCALE_EXPONENT = 4
SCALE_FACTORS = {Decimal(10) ** exponent: exponent
                 for exponent in range(MIN_SCALE_EXPONENT, MAX_SCALE_EXPONENT + 1)}
MAGNITUDE_RATIO = Decimal(10)


def decimals(value):
    return [Decimal(str(number)) for number in (value if isinstance(value, list) else [value])]


def scaled_values(source, scale):
    with localcontext() as context:
        values = decimals(source)
        context.prec = max(28, max(len(value.as_tuple().digits) for value in values))
        return [value.scaleb(SCALE_FACTORS[Decimal(str(scale))]) for value in values]


def quantity_text(value):
    def formatted(number):
        whole, dot, fraction = format(number, ',f').partition('.')
        fraction = fraction.rstrip('0')
        return whole + (dot + fraction if fraction else '')
    return '–'.join(formatted(number) for number in decimals(value))


def conversion_text(rule):
    if rule.get('scale') is None:
        return ''
    return (quantity_text(rule['value']) + '（原文 ' + quantity_text(rule['source_value'])
        + '，按 ×' + quantity_text(rule['scale']) + ' 换算）')


def magnitude_warning(value, history):
    if value is None or history is None:
        return None
    numbers = decimals(value)
    low, high = min(numbers), max(numbers)
    data_low, data_high = decimals([history['min'], history['max']])
    if low <= data_high and data_low <= high:
        return None
    with localcontext() as context:
        context.prec = 40
        pairs = ((abs((low + high) / 2), abs((data_low + data_high) / 2)),
                 (high - low, data_high - data_low))
        for pair in pairs:
            if all(pair) and max(pair) / min(pair) >= MAGNITUDE_RATIO:
                ratio = max(pair) / min(pair)
                break
        else:
            return None
    approx = format(ratio, '.1g')
    return (f'规则数值 {quantity_text(value)} 与该指标近 3 年 {quantity_text([data_low, data_high])} '
        f'相差约 {quantity_text(approx)} 倍，请核对原文写法或单位')
