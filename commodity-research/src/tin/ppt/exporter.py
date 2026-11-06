"""Surgical OPC assembly: new PNG parts and relationships, original media intact."""
from pathlib import Path
import posixpath
import shutil
from uuid import uuid4
from zipfile import ZIP_DEFLATED, ZipFile

from lxml import etree as ET

from tin.ppt.package import NS, PptError, package, qn, rels_part, xml, xml_bytes

PNG_SIGNATURE = b'\x89PNG\r\n\x1a\n'
MAX_PNG = 4 * 1024 * 1024
MAX_IMAGES = 400
AFTER_HIGHLIGHT = {qn('a', name) for name in (
    'uLnTx', 'uLn', 'uFillTx', 'uFill', 'latin', 'ea', 'cs', 'sym',
    'hlinkClick', 'hlinkMouseOver', 'rtl', 'extLst')}


def highlight_paragraph(paragraph):
    for run in paragraph:
        if run.tag not in {qn('a', 'r'), qn('a', 'fld')}:
            continue
        properties = run.find(qn('a', 'rPr'))
        if properties is None:
            properties = ET.Element(qn('a', 'rPr'))
            run.insert(0, properties)
        for previous in list(properties.findall(qn('a', 'highlight'))):
            properties.remove(previous)
        highlight = ET.Element(qn('a', 'highlight'))
        ET.SubElement(highlight, qn('a', 'srgbClr'), val='FFFF00')
        index = next((index for index, child in enumerate(properties) if child.tag in AFTER_HIGHLIGHT), len(properties))
        properties.insert(index, highlight)


def assemble(source, destination, presentation, replacements, *, mark_headlines=False):
    """Replacement values are validated PNG paths; only touched XML is serialized."""
    if len(replacements) > MAX_IMAGES:
        raise PptError('单次导出最多400张图片', 413)
    known = {slot['slot_id']: slot for slide in presentation['slides'] for slot in slide['slots']}
    for identifier, path in replacements.items():
        if identifier not in known or not known[identifier]['replaceable']:
            raise PptError('导出包含未知或不支持替换的图位')
        if Path(path).stat().st_size > MAX_PNG:
            raise PptError('单张PNG不能超过4MB', 413)
        with Path(path).open('rb') as stream:
            if stream.read(8) != PNG_SIGNATURE:
                raise PptError('图位图片必须是PNG')
    with package(source) as original:
        names = set(original.namelist())
        changes, media = {}, {}
        titles = []
        for slide in presentation['slides']:
            selected = [slot for slot in slide['slots'] if slot['slot_id'] in replacements]
            numeric = any(character.isdigit() for character in slide['title'])
            if numeric:
                titles.append({'slide': slide['number'], 'title': slide['title']})
            if not selected and not (numeric and mark_headlines):
                continue
            root = xml(original.read(slide['part']))
            if selected:
                relation_part = rels_part(slide['part'])
                relations = (xml(original.read(relation_part)) if relation_part in names
                             else ET.Element(qn('rel', 'Relationships'), nsmap={None: NS['rel']}))
                used = {item.get('Id') for item in relations}
                pictures = list(root.iter(qn('p', 'pic')))
                for slot in selected:
                    identifier = 'rIdPpt' + uuid4().hex
                    while identifier in used:
                        identifier = 'rIdPpt' + uuid4().hex
                    used.add(identifier)
                    path = 'ppt/media/ppt-' + uuid4().hex + '.png'
                    while path in names or path in media:
                        path = 'ppt/media/ppt-' + uuid4().hex + '.png'
                    media[path] = replacements[slot['slot_id']]
                    target = posixpath.relpath(path, posixpath.dirname(slide['part']))
                    ET.SubElement(relations, qn('rel', 'Relationship'), Id=identifier,
                                  Type=NS['r'] + '/image', Target=target)
                    try:
                        picture = pictures[slot['picture_index']]
                        blip = picture.find('./p:blipFill/a:blip', NS)
                        if blip is None or blip.get(qn('r', 'embed')) != slot['relationship_id']:
                            raise PptError('模板图位与原件不一致')
                        blip.set(qn('r', 'embed'), identifier)
                    except (IndexError, TypeError):
                        raise PptError('模板图位与原件不一致') from None
                changes[relation_part] = xml_bytes(relations)
            if numeric and mark_headlines:
                paragraphs = list(root.iter(qn('a', 'p')))
                highlight_paragraph(paragraphs[slide['title_paragraph']])
            changes[slide['part']] = xml_bytes(root)
        if media:
            types = xml(original.read('[Content_Types].xml'))
            defaults = types.findall(qn('ct', 'Default'))
            if not any(item.get('Extension', '').lower() == 'png' for item in defaults):
                default = ET.Element(qn('ct', 'Default'), Extension='png', ContentType='image/png')
                # CT_Types allows Defaults followed by Overrides.
                index = next((index for index, item in enumerate(types) if item.tag == qn('ct', 'Override')), len(types))
                types.insert(index, default)
                changes['[Content_Types].xml'] = xml_bytes(types)
        with ZipFile(destination, 'w', compression=ZIP_DEFLATED) as output:
            output.comment = original.comment
            for entry in original.infolist():
                if entry.filename in changes:
                    output.writestr(entry, changes.pop(entry.filename))
                else:
                    with original.open(entry) as reader, output.open(entry, 'w') as writer:
                        shutil.copyfileobj(reader, writer, length=1024 * 1024)
            for part, content in changes.items():
                output.writestr(part, content)
            for part, path in media.items():
                output.write(path, part)
        return titles
