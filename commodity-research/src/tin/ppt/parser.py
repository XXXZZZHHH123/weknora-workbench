"""Slide-order picture slots, local/group geometry and structural text evidence."""
from datetime import date
from decimal import Decimal, InvalidOperation
import re

from tin.ppt.emf import extract_text
from tin.ppt.package import PptError, NS, package, qn, relationships, xml

NUMBER = re.compile(r'[+-]?(?:(?:[0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.[0-9]+)?|\.[0-9]+)%?\Z')
ISO_DATE = re.compile(r'[0-9]{4}-[0-9]{2}-[0-9]{2}\Z')


def number(value):
    value = value.strip().replace('−', '-')
    if len(value) > 128 or not NUMBER.fullmatch(value):
        return None
    normalized = value.rstrip('%').replace(',', '')
    try:
        return Decimal(normalized), len(normalized.split('.')[1]) if '.' in normalized else 0
    except InvalidOperation:
        return None


def iso_date(value):
    if ISO_DATE.fullmatch(value):
        try:
            return date.fromisoformat(value).isoformat()
        except ValueError:
            pass
    return None


def classify(texts):
    pairs = []
    for index, text in enumerate(texts[:-1]):
        day, value = iso_date(text), number(texts[index + 1])
        if day and value is not None:
            pairs.append({'date': day, 'text': texts[index + 1],
                          'value': str(value[0]), 'decimals': value[1]})
    dates = sum(iso_date(text) is not None for text in texts)
    # At least three pairs and a majority of text segments participating in pairs.
    if dates >= 3 and len(pairs) >= 3 and len(pairs) * 2 > len(texts) / 2:
        return 'table', pairs
    return ('chart' if sum(number(text) is not None for text in texts) >= 3 else 'image'), []


def _integer(element, key):
    try:
        return int(element.get(key, '0')) if element is not None else 0
    except ValueError:
        raise PptError('PPT 形状坐标不是有效的 EMU 整数') from None


def geometry(shape):
    xfrm = shape.find('./p:spPr/a:xfrm', NS)
    if xfrm is None:
        xfrm = shape.find('./p:xfrm', NS)
    off = xfrm.find('a:off', NS) if xfrm is not None else None
    ext = xfrm.find('a:ext', NS) if xfrm is not None else None
    local = {'x': _integer(off, 'x'), 'y': _integer(off, 'y'),
             'cx': _integer(ext, 'cx'), 'cy': _integer(ext, 'cy')}
    # Preserve local EMUs and also expose the scaled page rectangle for a layout UI.
    page = dict(local)
    groups = []
    for parent in shape.iterancestors(qn('p', 'grpSp')):
        props = parent.find('./p:nvGrpSpPr/p:cNvPr', NS)
        groups.append(props.get('id') if props is not None else None)
        transform = parent.find('./p:grpSpPr/a:xfrm', NS)
        if transform is None:
            continue
        offset, extent = transform.find('a:off', NS), transform.find('a:ext', NS)
        child_offset, child_extent = transform.find('a:chOff', NS), transform.find('a:chExt', NS)
        for axis, size in (('x', 'cx'), ('y', 'cy')):
            denominator = _integer(child_extent, size)
            scale = _integer(extent, size) / denominator if denominator else 1
            page[axis] = _integer(offset, axis) + (page[axis] - _integer(child_offset, axis)) * scale
            page[size] *= scale
    return {'position': local, 'page_position': {key: round(value) for key, value in page.items()},
            'group_ids': groups}


def title_paragraph(root):
    for index, para in enumerate(root.iter(qn('a', 'p'))):
        text = ''.join(node.text or '' for node in para.iter(qn('a', 't'))).strip()
        if text:
            return index, text
    return None, ''


def parse_pptx(source):
    with package(source) as archive:
        presentation = xml(archive.read('ppt/presentation.xml'))
        if presentation.tag != qn('p', 'presentation'):
            raise PptError('PPT 缺少有效的演示文稿根节点')
        relations = relationships(archive, 'ppt/presentation.xml')
        size = presentation.find('p:sldSz', NS)
        pages = []
        for page_number, reference in enumerate(presentation.findall('./p:sldIdLst/p:sldId', NS), 1):
            relation = relations.get(reference.get(qn('r', 'id')), {})
            part = relation.get('target')
            if relation.get('type') != 'slide' or part not in archive.namelist():
                raise PptError('PPT 演示顺序引用的页面不存在')
            root = xml(archive.read(part))
            rels = relationships(archive, part)
            title_index, title = title_paragraph(root)
            slots, pic_index, chart_index = [], 0, 0
            for shape in root.iter():
                native = shape.tag == qn('p', 'graphicFrame') and shape.find('.//c:chart', NS) is not None
                if shape.tag != qn('p', 'pic') and not native:
                    continue
                if native:
                    chart_index += 1
                    blip = shape.find('.//c:chart', NS)
                    rid = blip.get(qn('r', 'id'))
                    slot_id = f'slide-{page_number}-chart-{chart_index}'
                    index = None
                else:
                    index = pic_index
                    pic_index += 1
                    blip = shape.find('./p:blipFill/a:blip', NS)
                    if blip is None:
                        continue
                    rid = blip.get(qn('r', 'embed'))
                    slot_id = f'slide-{page_number}-pic-{pic_index}'
                target = rels.get(rid, {}).get('target')
                props = shape.find('.//p:cNvPr', NS)
                svg = (bool(target and target.lower().endswith('.svg'))
                       or shape.find('.//svg:svgBlip', NS) is not None)
                reason = ('native_chart' if native else 'svg' if svg else
                          'missing_media' if target not in archive.namelist() else None)
                texts = extract_text(archive.read(target)) if (not reason and target.lower().endswith('.emf')) else []
                kind, pairs = classify(texts) if not reason else ('image', [])
                slots.append({'slot_id': slot_id, 'slide': page_number, 'slide_part': part,
                    'shape_id': props.get('id') if props is not None else None,
                    'name': props.get('name', '') if props is not None else '',
                    'picture_index': index, 'relationship_id': rid, 'media_part': target,
                    'kind': kind, 'texts': texts, 'pairs': pairs,
                    'replaceable': reason is None, 'preserve_reason': reason, **geometry(shape)})
            pages.append({'number': page_number, 'part': part, 'title': title,
                          'title_paragraph': title_index, 'slots': slots})
        return {'width_emu': _integer(size, 'cx'), 'height_emu': _integer(size, 'cy'), 'slides': pages}
