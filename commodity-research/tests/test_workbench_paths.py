import pytest

from tin.integration.workbench_paths import parse_workbench_path


IDENTIFIER = '34c424e8-42a0-4f89-9d52-d3bfcad34867'
ROOT = '/workbenches/' + IDENTIFIER


@pytest.mark.parametrize('suffix', ['', '/', '/sn', '/sn/', '/sn/indicators/实际字段',
                                    '/sn/indicators/field.name', '/api/sn', '/api/sn/',
                                    '/api/sn/series/123/history'])
def test_scoped_paths_preserve_only_the_current_workbench(suffix):
    assert parse_workbench_path(ROOT + suffix) == (IDENTIFIER, suffix or '/')


@pytest.mark.parametrize('path', ['', '/', '/sn/diagram', '/static/app.js', '/api/workbenches/id/overview'])
def test_other_route_families_are_not_scoped_paths(path):
    assert parse_workbench_path(path) is None


@pytest.mark.parametrize('value', [None, 1, True, [], {}, b'/workbenches/id'])
def test_invalid_path_types_are_rejected(value):
    with pytest.raises(ValueError):
        parse_workbench_path(value)


@pytest.mark.parametrize('path', [
    '/workbenches', '/workbenches/', '/workbenches-extra',
    '/workbenches/' + IDENTIFIER.upper(),
    '/workbenches/' + IDENTIFIER.replace('-', ''),
    '/workbenches/00000000-0000-0000-0000-000000000000',
    ROOT + '//sn', ROOT + '/sn//indicators', ROOT + '/sn/../api/sn', ROOT + '/sn/./indicators',
    ROOT + '/sn/%2e%2e', ROOT + '/sn/%252e%252e', ROOT + '/sn/field%2fname',
    ROOT + '/sn\\indicators', ROOT + '/sn?date=fixture', ROOT + '/sn#fragment',
    ROOT + '/api/library', ROOT + '/auth/session', ROOT + '/sn-other', ROOT + '/api/sn-other',
    ROOT + '/workbenches/' + IDENTIFIER,
])
def test_malformed_scoped_paths_never_fall_back_to_legacy(path):
    with pytest.raises(ValueError):
        parse_workbench_path(path)


@pytest.mark.parametrize('codepoint', [0, 9, 10, 31, 127, 128, 133, 159])
def test_control_characters_are_rejected(codepoint):
    with pytest.raises(ValueError):
        parse_workbench_path(ROOT + '/sn/field' + chr(codepoint))
