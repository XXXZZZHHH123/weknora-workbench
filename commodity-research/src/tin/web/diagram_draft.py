"""Compile an explicitly referenced model graph into the existing MD format."""
import logging
from collections import defaultdict
from typing import Annotated

from fastapi import HTTPException
from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator

from tin.export import diagram_md

logger = logging.getLogger(__name__)
Identifier = Annotated[str, Field(min_length=1, max_length=64, pattern=r'^[A-Za-z][A-Za-z0-9_-]*$')]
Label = Annotated[str, Field(min_length=1, max_length=160)]


class DraftObject(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)


def reject(kind, message):
    logger.warning('Diagram graph rejected: class=%s', kind)
    raise HTTPException(422, message)


def plain_text(value):
    if value.splitlines() != [value]:
        raise ValueError('labels must be a single line')
    value = value.strip()
    if not value or any(ord(c) < 32 for c in value) or '<!--' in value or '-->' in value:
        raise ValueError('invalid plain label')
    return value


class Group(DraftObject):
    id: Identifier
    label: Label
    parent_id: Identifier | None = None

    _plain_label = field_validator('label')(plain_text)


class Node(DraftObject):
    id: Identifier
    label: Label
    group_id: Identifier
    series_id: Annotated[str, Field(min_length=1, max_length=128)] | None = None

    @field_validator('label')
    @classmethod
    def node_label(cls, value):
        value = plain_text(value)
        if any(token in value for token in ('[', ']', '->', '=>', '→')):
            raise ValueError('relationships belong in edges, not node labels')
        return value


class Edge(DraftObject):
    from_id: Identifier = Field(alias='from')
    to: Identifier


class Graph(DraftObject):
    title: Label
    groups: Annotated[list[Group], Field(min_length=1, max_length=128)]
    nodes: Annotated[list[Node], Field(min_length=1, max_length=256)]
    edges: Annotated[list[Edge], Field(max_length=512)]

    _plain_title = field_validator('title')(plain_text)


def compile_graph(document):
    try:
        graph = Graph.model_validate(document)
    except ValidationError:
        reject('invalid_schema', '模型生成的图结构格式不完整，请重新生成')
    objects = [*graph.groups, *graph.nodes]
    if len(objects) > 256:
        reject('too_many_nodes', '产业图超过256个节点和分组，请缩小范围')
    identifiers = {item.id for item in objects}
    if len(identifiers) != len(objects):
        reject('duplicate_id', '模型生成的节点编号重复，请重新生成')
    groups = {item.id: item for item in graph.groups}
    children = defaultdict(list)
    members = defaultdict(list)
    depths = {}

    def depth(identifier, ancestors):
        if identifier in depths:
            return depths[identifier]
        if identifier in ancestors:
            reject('group_cycle', '模型生成的分组层级存在循环，请重新生成')
        parent = groups[identifier].parent_id
        if parent is not None and parent not in groups:
            reject('unknown_parent', '模型生成的上级分组不存在，请重新生成')
        value = 0 if parent is None else depth(parent, ancestors | {identifier}) + 1
        if value > 2:
            reject('group_depth', '产业图分组超过三级，请简化层级后重新生成')
        depths[identifier] = value
        return value

    for group in graph.groups:
        depth(group.id, set())
        children[group.parent_id].append(group)
    populated = set()
    for node in graph.nodes:
        if node.group_id not in groups:
            reject('unknown_group', '模型生成的节点所属分组不存在，请重新生成')
        members[node.group_id].append(node)
        parent = node.group_id
        while parent is not None:
            populated.add(parent)
            parent = groups[parent].parent_id
    if populated != groups.keys():
        reject('empty_group', '模型生成了没有节点的分组，请重新生成')
    edge_pairs = list(dict.fromkeys((edge.from_id, edge.to) for edge in graph.edges))
    if any(a not in identifiers or b not in identifiers for a, b in edge_pairs):
        reject('unknown_endpoint', '模型生成的连线端点不存在，请重新生成')

    lines = [f'# {graph.title}', '']

    def emit(group):
        lines.append(f'{"#" * (2 + depths[group.id])} {group.label} <!-- id: {group.id} -->')
        for node in members[group.id]:
            binding = f' <!-- bind: {node.series_id} -->' if node.series_id else ''
            lines.append(f'- {node.label} <!-- id: {node.id} -->{binding}')
        lines.append('')
        for child in children[group.id]:
            emit(child)

    for group in children[None]:
        emit(group)
    lines.extend(f'@{a} -> @{b}' for a, b in edge_pairs)
    markdown = '\n'.join(lines).rstrip() + '\n'
    if len(markdown.encode('utf-8')) > 64000:
        reject('too_large', '生成的产业图配置过大，请缩小范围')
    try:
        restored = diagram_md.parse(markdown)
    except (diagram_md.MarkdownError, ValueError, RecursionError):
        reject('roundtrip_parse', '图结构转换失败，未应用到当前工作台')
    actual = {node['id']: node for node in restored['nodes']}
    matches = actual.keys() == identifiers and restored.get('name') == graph.title
    for group in graph.groups:
        node = actual.get(group.id, {})
        matches &= (node.get('kind') == 'group' and node.get('label') == group.label
                    and node.get('parent') == group.parent_id)
    for item in graph.nodes:
        node = actual.get(item.id, {})
        binding = {'kind': 'series', 'series_id': item.series_id} if item.series_id else {'kind': 'none'}
        matches &= (node.get('kind') != 'group' and node.get('label') == item.label
                    and node.get('parent') == item.group_id and node.get('binding') == binding)
    matches &= {(edge['from'], edge['to']) for edge in restored['edges']} == set(edge_pairs)
    if not matches:
        reject('roundtrip_topology', '图结构转换后层级或连线不一致，未应用到当前工作台')
    return markdown
