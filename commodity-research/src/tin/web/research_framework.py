"""Research framework shared by diagram outlines and core-focus suggestions.

These are instructions for a model to interpret evidence, never programmatic
rules for classifying or selecting catalog fields.
"""

RESEARCH_GROUPS = ('核心标的', '供给', '需求', '库存', '平衡', '宏观')

RESEARCH_FRAMEWORK = (
    '\n【研究框架】六个分组：核心标的、供给、需求、库存、平衡、宏观。'
    '\n- 核心标的：只放描述标的价格状态的概念，按维度各建一个：国内价格、海外价格、现货升贴水、'
    '期限结构（近远月价差）、内外比价。名称写成“标的名+维度”。'
    '\n- 供给、需求：具体对象由资料决定。'
    '\n- 库存：资料提到的库存口径都放这里；资料没有提到时，建“交易所库存”和“社会库存”两个概念。'
    '\n- 平衡：用不带地域和年份的通用量，建“供给量”“消费量”“供需差”“库存变化”；'
    '资料里的年度、全球数字只是背景，不写进名称。'
    '\n- 宏观：只建可观测的外部变量（指数、利率、汇率、相关商品价格等），不建事件或政策。'
    '资料提到的宏观驱动优先；资料没有提到时，从可用宏观指标中选与该品种研究最相关的2到4个，名称用指标的通用叫法。'
)
