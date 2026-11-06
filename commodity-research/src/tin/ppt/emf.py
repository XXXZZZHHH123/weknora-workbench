"""Read text records, never execute or render the EMF graphics program."""
import struct

MAX_TEXTS = 200
MAX_TEXT_CHARS = 4096


def extract_text(data, limit=MAX_TEXTS):
    result = []

    def append(start, count, end):
        if 0 < count <= MAX_TEXT_CHARS and start >= 0 and start + count * 2 <= end:
            value = data[start:start + count * 2].decode('utf-16le', errors='replace').strip()
            if value:
                result.append(value)

    offset = 0
    limit = min(limit, MAX_TEXTS)
    while offset + 8 <= len(data) and len(result) < limit:
        kind, size = struct.unpack_from('<II', data, offset)
        end = offset + size
        if size < 8 or size % 4 or end > len(data):
            break
        if kind == 84 and size >= 76:  # EMR_EXTTEXTOUTW: EMRTEXT at byte 36.
            count, relative = struct.unpack_from('<II', data, offset + 44)
            if relative >= 76:
                append(offset + relative, count, end)
        elif kind == 70 and size >= 16:  # EMR_GDICOMMENT: cbData then EMF+ signature.
            count = struct.unpack_from('<I', data, offset + 8)[0]
            comment_end = offset + 12 + count
            if 4 <= count <= size - 12 and data[offset + 12:offset + 16] == b'EMF+':
                cursor = offset + 16
                while cursor + 12 <= comment_end and len(result) < limit:
                    record, _, length, payload = struct.unpack_from('<HHII', data, cursor)
                    if length < 12 or length % 4 or cursor + length > comment_end or payload > length - 12:
                        break
                    if record == 0x401C and payload >= 28:  # EmfPlusDrawString.
                        chars = struct.unpack_from('<I', data, cursor + 20)[0]
                        append(cursor + 40, chars, cursor + 12 + payload)
                    cursor += length
        offset = end
    return result
