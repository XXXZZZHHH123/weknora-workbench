"""Entirely synthetic OPC packages and EMF records; no business files."""
import base64
from io import BytesIO
import struct
from zipfile import ZIP_DEFLATED, ZipFile

from lxml import etree as ET

from tin.ppt.package import NS, qn

PNG = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=')
GRAPH_TEXTS = ['隔离图表', '合成曲线', '段甲', '件', '0', '10', '20']
TABLE_TEXTS = ['合成小表', '2023-01-01', '10.00', '2023-01-02', '11.0',
               '2023-01-03', '12', '2023-01-04', '13', '2023-01-05', '14']


def ext_text(value):
    encoded = value.encode('utf-16le')
    size = (76 + len(encoded) + 3) // 4 * 4
    record = bytearray(size)
    struct.pack_into('<II', record, 0, 84, size)
    struct.pack_into('<II', record, 44, len(encoded) // 2, 76)
    record[76:76 + len(encoded)] = encoded
    return bytes(record)


def plus_text(value):
    encoded = value.encode('utf-16le')
    payload = struct.pack('<IIIffff', 0, 0, len(encoded) // 2, 0, 0, 100, 20) + encoded
    size = (12 + len(payload) + 3) // 4 * 4
    record = struct.pack('<HHII', 0x401C, 0, size, len(payload)) + payload
    record += b'\0' * (size - len(record))
    comment = b'EMF+' + record
    return struct.pack('<III', 70, 12 + len(comment), len(comment)) + comment


def emf(texts, *, plus=False):
    # A valid minimum EMR_HEADER followed by text and EMR_EOF.
    header = bytearray(88)
    struct.pack_into('<II', header, 0, 1, 88)
    struct.pack_into('<II', header, 40, 0x464D4520, 0x10000)
    records = b''.join((plus_text if plus else ext_text)(text) for text in texts)
    end = struct.pack('<IIIII', 14, 20, 0, 0, 20)
    struct.pack_into('<II', header, 48, len(header) + len(records) + len(end), len(texts) + 2)
    return bytes(header) + records + end


def serialize(root):
    return ET.tostring(root, encoding='UTF-8', xml_declaration=True)


def archive_bytes(entries):
    output = BytesIO()
    with ZipFile(output, 'w', ZIP_DEFLATED) as archive:
        archive.comment = b'synthetic OPC package'
        for name, data in entries.items():
            archive.writestr(name, data)
    return output.getvalue()


def _sub(parent, prefix, tag, **attrs):
    return ET.SubElement(parent, qn(prefix, tag), **attrs)


def _transform(parent, *, group=False):
    xfrm = _sub(parent, 'a', 'xfrm')
    _sub(xfrm, 'a', 'off', x='100', y='200')
    _sub(xfrm, 'a', 'ext', cx='300', cy='400')
    if group:
        _sub(xfrm, 'a', 'chOff', x='10', y='20')
        _sub(xfrm, 'a', 'chExt', cx='150', cy='200')


def _picture(parent, shape_id, rid, *, svg=False):
    picture = _sub(parent, 'p', 'pic')
    props = _sub(picture, 'p', 'nvPicPr')
    _sub(props, 'p', 'cNvPr', id=str(shape_id), name='Picture ' + str(shape_id))
    _sub(props, 'p', 'cNvPicPr')
    _sub(props, 'p', 'nvPr')
    fill = _sub(picture, 'p', 'blipFill')
    blip = _sub(fill, 'a', 'blip')
    blip.set(qn('r', 'embed'), rid)
    if svg:
        extension = _sub(_sub(blip, 'a', 'extLst'), 'a', 'ext', uri='{SVG-FIXTURE}')
        _sub(extension, 'svg', 'svgBlip').set(qn('r', 'embed'), 'rSvg')
    _sub(fill, 'a', 'srcRect', l='100', t='200', r='300', b='400')
    _sub(_sub(fill, 'a', 'stretch'), 'a', 'fillRect')
    _transform(_sub(picture, 'p', 'spPr'))
    return picture


def _slide(title, *, full=False, many=0):
    slide = ET.Element(qn('p', 'sld'), nsmap={key: NS[key] for key in ('p', 'a', 'r', 'c', 'svg')})
    tree = _sub(_sub(slide, 'p', 'cSld'), 'p', 'spTree')
    props = _sub(tree, 'p', 'nvGrpSpPr')
    _sub(props, 'p', 'cNvPr', id='1', name='')
    _sub(props, 'p', 'cNvGrpSpPr')
    _sub(props, 'p', 'nvPr')
    _sub(tree, 'p', 'grpSpPr')
    title_shape = _sub(tree, 'p', 'sp')
    text = _sub(title_shape, 'p', 'txBody')
    _sub(text, 'a', 'bodyPr')
    _sub(text, 'a', 'lstStyle')
    _sub(text, 'a', 'p')  # A leading empty paragraph must not become the title.
    para = _sub(text, 'a', 'p')
    run = _sub(para, 'a', 'r')
    style = _sub(run, 'a', 'rPr', lang='zh-CN')
    _sub(style, 'a', 'ln')
    _sub(_sub(style, 'a', 'solidFill'), 'a', 'srgbClr', val='000000')
    _sub(style, 'a', 'effectLst')
    _sub(style, 'a', 'uLnTx')
    _sub(style, 'a', 'uFillTx')
    _sub(style, 'a', 'latin', typeface='Arial')
    _sub(style, 'a', 'ea', typeface='Arial')
    _sub(style, 'a', 'cs', typeface='Arial')
    _sub(style, 'a', 'sym', typeface='Symbol')
    _sub(style, 'a', 'hlinkClick')
    _sub(style, 'a', 'hlinkMouseOver')
    _sub(style, 'a', 'rtl', val='0')
    _sub(style, 'a', 'extLst')
    _sub(run, 'a', 't').text = title
    _sub(_sub(para, 'a', 'r'), 'a', 't').text = ' 后半段'  # No a:rPr.
    field = _sub(para, 'a', 'fld', id='{00000000-0000-0000-0000-000000000001}')
    _sub(field, 'a', 't').text = ' 页码'
    _sub(_sub(_sub(text, 'a', 'p'), 'a', 'r'), 'a', 't').text = '正文数字 12345'
    if full:
        group = _sub(tree, 'p', 'grpSp')
        _sub(_sub(group, 'p', 'nvGrpSpPr'), 'p', 'cNvPr', id='2', name='Group')
        _transform(_sub(group, 'p', 'grpSpPr'), group=True)
        _picture(group, 3, 'rGraph')
        _picture(tree, 4, 'rTable')
        _picture(tree, 5, 'rShared')
        _picture(tree, 6, 'rSvg')
        _picture(tree, 7, 'rShared', svg=True)
        native = _sub(tree, 'p', 'graphicFrame')
        _sub(_sub(native, 'p', 'nvGraphicFramePr'), 'p', 'cNvPr', id='8', name='Native chart')
        _sub(_sub(_sub(native, 'a', 'graphic'), 'a', 'graphicData'), 'c', 'chart').set(qn('r', 'id'), 'rChart')
        _picture(tree, 9, 'rUnbound')
    else:
        _picture(tree, 3, 'rGraph')  # Shared EMF; changes on the first page must not affect it.
        _picture(tree, 4, 'rShared')
    for index in range(many):
        _picture(tree, 10 + index, 'rShared')
    return serialize(slide)


def _relationships(values):
    root = ET.Element(qn('rel', 'Relationships'), nsmap={None: NS['rel']})
    for identifier, kind, target in values:
        _sub(root, 'rel', 'Relationship', Id=identifier, Type=NS['r'] + '/' + kind, Target=target)
    return serialize(root)


def ppt_entries(*, graph_texts=None, table_texts=None, many=0, png_default=False):
    presentation = ET.Element(qn('p', 'presentation'), nsmap={'p': NS['p'], 'r': NS['r']})
    slides = _sub(presentation, 'p', 'sldIdLst')
    for index, rid in enumerate(('rFirst', 'rSecond'), 256):
        _sub(slides, 'p', 'sldId', id=str(index)).set(qn('r', 'id'), rid)
    _sub(presentation, 'p', 'sldSz', cx='12000000', cy='9000000')
    types = ET.Element(qn('ct', 'Types'), nsmap={None: NS['ct']})
    for extension, content_type in [('xml', 'application/xml'), ('rels', 'application/vnd.openxmlformats-package.relationships+xml'),
                                    ('emf', 'image/x-emf'), ('svg', 'image/svg+xml'), ('jpg', 'image/jpeg')]:
        _sub(types, 'ct', 'Default', Extension=extension, ContentType=content_type)
    if png_default:
        _sub(types, 'ct', 'Default', Extension='png', ContentType='image/png')
    for part, content_type in [('presentation.xml', 'presentationml.presentation.main'),
                               ('slides/slide7.xml', 'presentationml.slide'), ('slides/slide2.xml', 'presentationml.slide'),
                               ('charts/chart1.xml', 'drawingml.chart')]:
        _sub(types, 'ct', 'Override', PartName='/ppt/' + part,
             ContentType='application/vnd.openxmlformats-officedocument.' + content_type + '+xml')
    rels = [('rGraph', 'image', '../media/shared.emf'), ('rTable', 'image', '../media/table.emf'),
            ('rShared', 'image', '../media/shared.png'), ('rSvg', 'image', '../media/vector.svg'),
            ('rChart', 'chart', '../charts/chart1.xml'), ('rUnbound', 'image', '../media/unbound.jpg')]
    return {
        '[Content_Types].xml': serialize(types),
        '_rels/.rels': _relationships([('rOffice', 'officeDocument', 'ppt/presentation.xml')]),
        'ppt/presentation.xml': serialize(presentation),
        'ppt/_rels/presentation.xml.rels': _relationships([('rFirst', 'slide', 'slides/slide7.xml'),
                                                         ('rSecond', 'slide', 'slides/slide2.xml')]),
        'ppt/slides/slide7.xml': _slide('合成周报 2030', full=True, many=many),
        'ppt/slides/slide2.xml': _slide('另一页'),
        'ppt/slides/_rels/slide7.xml.rels': _relationships(rels),
        'ppt/slides/_rels/slide2.xml.rels': _relationships(rels),
        'ppt/media/shared.emf': emf(GRAPH_TEXTS if graph_texts is None else graph_texts),
        'ppt/media/table.emf': emf(TABLE_TEXTS if table_texts is None else table_texts, plus=True),
        'ppt/media/shared.png': PNG,
        'ppt/media/vector.svg': b'<svg xmlns="http://www.w3.org/2000/svg"><rect width="2" height="2"/></svg>',
        'ppt/media/unbound.jpg': b'synthetic-jpeg-preserved-verbatim',
        'ppt/charts/chart1.xml': b'<c:chartSpace xmlns:c="' + NS['c'].encode() + b'"><c:chart/></c:chartSpace>',
        'custom/untouched.bin': b'\x00\x01\xffall bytes must survive\r\n',
    }


def make_pptx(**kwargs):
    return archive_bytes(ppt_entries(**kwargs))
