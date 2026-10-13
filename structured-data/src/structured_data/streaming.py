"""Disk-backed parser records, preserving the existing JSON source contract."""
import json
from itertools import islice
from pathlib import Path


class DiskRows:
    """Append and replay records without retaining the workbook in Python memory."""
    def __init__(self, path):
        self.path = Path(path)
        self.writer = self.path.open('w', encoding='utf-8')
        self.count = 0

    def append(self, value):
        self.writer.write(json.dumps(value, ensure_ascii=False, default=str) + '\n')
        self.count += 1

    def __len__(self):
        return self.count

    def __iter__(self):
        self.writer.flush()
        with self.path.open(encoding='utf-8') as stream:
            for line in stream:
                yield json.loads(line)

    def __getitem__(self, key):
        if not isinstance(key, slice) or key.step not in (None, 1):
            raise TypeError('Only bounded forward slices are supported')
        return list(islice(self, key.start or 0, key.stop))

    def close(self):
        self.writer.close()


def read_json_array(path):
    """Read legacy issues arrays one object at a time, with bounded buffers."""
    decoder = json.JSONDecoder()
    with Path(path).open(encoding='utf-8') as stream:
        buffer = ''
        finished = False
        started = False
        while True:
            if not finished:
                chunk = stream.read(65536)
                buffer += chunk
                finished = not chunk
            buffer = buffer.lstrip()
            if not started:
                if not buffer and not finished:
                    continue
                if not buffer.startswith('['):
                    raise ValueError('Invalid source issues array')
                buffer = buffer[1:]
                started = True
            while True:
                buffer = buffer.lstrip()
                if buffer.startswith(','):
                    buffer = buffer[1:].lstrip()
                if buffer.startswith(']'):
                    return
                try:
                    value, end = decoder.raw_decode(buffer)
                except json.JSONDecodeError:
                    if finished:
                        raise ValueError('Incomplete source issues array') from None
                    break
                yield value
                buffer = buffer[end:]


def write_json_array(path, values):
    count = 0
    with Path(path).open('w', encoding='utf-8') as stream:
        stream.write('[')
        for value in values:
            if count:
                stream.write(',\n')
            stream.write(json.dumps(value, ensure_ascii=False, default=str))
            count += 1
        stream.write(']')
    return count
