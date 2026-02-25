"""Tests for refactor-script string transforms.

Run with: python -m pytest scripts/refactor/test_transforms.py -q
"""
import re

from check_imports import has_testutil_in_imports


# --- check_imports: has_testutil_in_imports ---

def test_detects_single_import():
    src = 'import "github.com/roborev-dev/roborev/internal/testutil"\n'
    assert has_testutil_in_imports(src)


def test_detects_import_in_block():
    src = (
        'import (\n'
        '\t"testing"\n'
        '\n'
        '\t"github.com/roborev-dev/roborev/internal/testutil"\n'
        ')\n'
    )
    assert has_testutil_in_imports(src)


def test_ignores_testutil_in_comment():
    src = (
        'import (\n'
        '\t"testing"\n'
        ')\n'
        '\n'
        '// uses testutil for helpers\n'
        'func Foo() {}\n'
    )
    assert not has_testutil_in_imports(src)


def test_ignores_testutil_in_string_literal():
    src = (
        'import (\n'
        '\t"testing"\n'
        ')\n'
        '\n'
        'var s = "testutil"\n'
    )
    assert not has_testutil_in_imports(src)


def test_ignores_testutil_in_function_name():
    src = (
        'import "testing"\n'
        '\n'
        'func testutil_helper() {}\n'
    )
    assert not has_testutil_in_imports(src)


# --- update_testutil: env-var replacement regex ---

def _apply_env_replacement(content: str) -> str:
    """Apply the same env-var regex logic as update_testutil.py."""
    env_replacements = [
        ("GIT_AUTHOR_NAME", "GitUserName"),
        ("GIT_AUTHOR_EMAIL", "GitUserEmail"),
        ("GIT_COMMITTER_NAME", "GitUserName"),
        ("GIT_COMMITTER_EMAIL", "GitUserEmail"),
    ]
    for env_key, const_name in env_replacements:
        pat = re.compile(
            r'"' + re.escape(env_key) + r'='
            + r'[^"]*"'
            + r'(\s*,?)'
        )
        replacement = '"' + env_key + '="+' + const_name + r'\1'
        content = pat.sub(replacement, content)
    return content


def test_env_replacement_standard_form():
    go_src = '"GIT_AUTHOR_NAME=Test",'
    result = _apply_env_replacement(go_src)
    assert result == '"GIT_AUTHOR_NAME="+GitUserName,'


def test_env_replacement_with_email():
    go_src = '"GIT_AUTHOR_EMAIL=test@test.com",'
    result = _apply_env_replacement(go_src)
    assert result == '"GIT_AUTHOR_EMAIL="+GitUserEmail,'


def test_env_replacement_no_trailing_comma():
    go_src = '"GIT_COMMITTER_NAME=Test"'
    result = _apply_env_replacement(go_src)
    assert result == '"GIT_COMMITTER_NAME="+GitUserName'


def test_env_replacement_extra_spaces_before_comma():
    go_src = '"GIT_COMMITTER_EMAIL=test@test.com"  ,'
    result = _apply_env_replacement(go_src)
    assert result == '"GIT_COMMITTER_EMAIL="+GitUserEmail  ,'


def test_env_replacement_all_four_keys():
    go_src = (
        '\t"GIT_AUTHOR_NAME=Test",\n'
        '\t"GIT_AUTHOR_EMAIL=test@test.com",\n'
        '\t"GIT_COMMITTER_NAME=Test",\n'
        '\t"GIT_COMMITTER_EMAIL=test@test.com",\n'
    )
    result = _apply_env_replacement(go_src)
    assert '"GIT_AUTHOR_NAME="+GitUserName,' in result
    assert '"GIT_AUTHOR_EMAIL="+GitUserEmail,' in result
    assert '"GIT_COMMITTER_NAME="+GitUserName,' in result
    assert '"GIT_COMMITTER_EMAIL="+GitUserEmail,' in result


def test_env_replacement_produces_valid_go_concat():
    """Verify the output is syntactically valid Go string concatenation."""
    go_src = '"GIT_AUTHOR_NAME=Test",'
    result = _apply_env_replacement(go_src)
    # Should produce: "KEY=" + Const, — valid Go
    # Should NOT produce: "KEY="+Const" — double-quoted mess
    assert result.count('"') == 2  # opening and closing of "KEY="
    assert '""' not in result  # no empty-string artifacts
    assert '"+' in result  # proper concatenation operator
