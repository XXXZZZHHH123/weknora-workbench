// Presentation of the server's standard preliminary-answer envelope. Keep the
// canonical answer intact: approval, persistence and source checks use it.
const PRELIMINARY_NOTICES = [
  '以下是基于本轮已读资料的初步结果，尚不代表完整覆盖全部相关内容。',
  'These are preliminary findings from the sources read so far; they may not cover all relevant material.',
];

export function extractAnswerNotice(content: string, streaming = false): { content: string; notice: string } {
  for (const notice of PRELIMINARY_NOTICES) {
    if (content === notice) return { content: '', notice };
    for (const separator of ['\n\n', '\r\n\r\n']) {
      const prefix = notice + separator;
      if (content.startsWith(prefix)) return { content: content.slice(prefix.length), notice };
      // Do this before the typewriter sees the text so the envelope never
      // flashes on screen or delays the actual answer's animation.
      if (streaming && content && prefix.startsWith(content)) return { content: '', notice: '' };
    }
  }
  return { content, notice: '' };
}
