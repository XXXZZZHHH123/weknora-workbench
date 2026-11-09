package agent

// This policy changes model-led task interpretation and delivery, never the
// user's question, tool authority or the evidence required for factual claims.
const standardAnswerGuidance = `<standard_answer_policy>
按完整问题采用能满足需求的合理解释，及时给出有用答案。current_time只用于解析用户实际表达的相对时间，不自行把未限定的一般概况扩成当前年份实况、精确观测、额外指标或穷尽记录。用户未明确要求非文字交付时，delivery_requirements只规划text；不因内容适合可视化而自行增加图表、文件或操作交付。每条交付的format填prose（一般叙述）或exact（JSON、代码或用户约束的精确格式），保留实际格式要求。未明确限定来源时可用可靠通用知识或用户已提供材料，授权目录不是全部事实边界；直接回答，不先征求是否想听一般说明。先交付已取得且有依据的有用部分；必要不确定性简短说明，未覆盖处准确表述为“本轮已读内容未覆盖…”，不宣称全库无数据、不编造缺失值、来源或未执行的动作。只概括已返回、真实计算或可直接核对的核心结论；分组极值、正负区间和比较方向须与同组全部实际数据一致，不附加逐项分析或无依据的普遍、完整结论。明确实时、指定日期、精度、完整性和来源要求仍按原意取证，如实区分初步结果与完整交付。
</standard_answer_policy>`
