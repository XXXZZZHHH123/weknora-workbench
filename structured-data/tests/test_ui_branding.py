import json

from structured_data.ui_config import read_ui_config


def test_existing_pilot_presentation_uses_current_brand_and_ordinary_chat(tmp_path):
    config = {'title':'结构化数据工作台', 'workspace_label':'Excel 数据管理',
              'agent_name':'表格数据分析（结构化）',
              'weknora_url':'https://research.example/platform/agents'}
    path = tmp_path/'workbench-config.json'
    raw = json.dumps(config)
    path.write_text(raw)
    result = read_ui_config(tmp_path)
    assert result['title'] == '表格分析'
    assert result['workspace_label'] == '中哲物产 · 研究工作台'
    assert result['weknora_url'] == 'https://research.example/platform/creatChat'
    assert path.read_text() == raw


def test_workspace_customizations_are_preserved(tmp_path):
    config = {'title':'能源研究数据', 'workspace_label':'能源研究组',
              'weknora_url':'https://research.example/platform/creatChat'}
    (tmp_path/'workbench-config.json').write_text(json.dumps(config))
    result = read_ui_config(tmp_path)
    for key, value in config.items():
        assert result[key] == value
