"""Deterministic TOML transformation tests; no Voxtype or service access."""

import copy
import math
from pathlib import Path
import sys
import tomllib
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from voxtype_config import configure


class ConfigureTests(unittest.TestCase):
    endpoint = "http://127.0.0.1:8377"

    def assert_semantics_equal(self, left, right):
        self.assertIs(type(left), type(right))
        if isinstance(left, dict):
            self.assertEqual(left.keys(), right.keys())
            for key in left:
                self.assert_semantics_equal(left[key], right[key])
        elif isinstance(left, list):
            self.assertEqual(len(left), len(right))
            for a, b in zip(left, right):
                self.assert_semantics_equal(a, b)
        elif isinstance(left, float) and math.isnan(left):
            self.assertTrue(math.isnan(right))
        else:
            self.assertEqual(left, right)

    def transform(self, source):
        original = tomllib.loads(source)
        result = configure(source, self.endpoint)
        expected = copy.deepcopy(original)
        expected["engine"] = "whisper"
        whisper = expected.setdefault("whisper", {})
        whisper.update(mode="remote", remote_endpoint=self.endpoint,
                       remote_model="whisper-1", remote_api_key="local-placeholder")
        whisper.setdefault("language", "auto")
        self.assert_semantics_equal(expected, tomllib.loads(result))
        self.assertEqual(result, configure(result, self.endpoint))
        return result

    def test_empty_and_comment_only_config(self):
        for source in ("", "# my configuration", "# retained\n\n", "\n\n"):
            with self.subTest(source=source):
                result = self.transform(source)
                self.assertIn(source, result)

    def test_preserves_real_preferences_and_comments(self):
        source = '''# Desktop preferences
engine = "parakeet" # selected backend
[hotkey]
key = "F9"
modifiers = ["CTRL", "ALT"]
[input]
device = "my microphone"
sample_rate = 48000
[whisper] # local model stays available
model = "large-v3"
mode = "local" # connection mode
language = "pl"
remote_endpoint = 'https://old.invalid'
remote_model = "old"
remote_api_key = "old-key"
[output]
mode = "paste"
paste_keys = "ctrl+shift+v"
auto_submit = false
smart_auto_submit = true
post_process_command = "my-existing-paste-hook --stdin"
'''
        result = self.transform(source)
        self.assertIn('engine = "whisper" # selected backend', result)
        self.assertIn('mode = "remote" # connection mode', result)
        for fragment in ('# Desktop preferences', '[whisper] # local model stays available',
                         'model = "large-v3"', 'language = "pl"',
                         source[source.index('[output]'):],
                         source[source.index('[hotkey]'):source.index('[whisper]')]):
            self.assertIn(fragment, result)

    def test_missing_settings_in_explicit_table(self):
        self.transform('engine = "whisper"\n[whisper]\nmodel = "tiny"\n[output]\nmode = "clipboard"\n')
        self.transform('[whisper] # no final newline')

    def test_dotted_and_quoted_keys(self):
        source = '''"engine" = 'local'
"whisper" . 'mode' = "local" # retain this
whisper.remote_endpoint = "old"
whisper."remote_model" = 'old'
whisper.'remote_api_key' = "old"
whisper.language = "de"
whisper.model = "tiny"
output.auto_submit = false
output.paste_keys = "ctrl+v"
'''
        result = self.transform(source)
        self.assertIn('"whisper" . \'mode\' = "remote" # retain this', result)
        self.assertIn('output.auto_submit = false', result)
        self.assertNotIn('[whisper]', result)

    def test_quoted_section_and_implicit_parent(self):
        for source in (
            '[ "whisper" ] # spelling stays\n"mode" = "local"\nlanguage = "fr"\n',
            '[whisper.decoder]\nthreads = 4\n[output]\nmode = "paste"\n',
            '["whisper".\'decoder\']\nthreads = 2\n',
        ):
            with self.subTest(source=source):
                result = self.transform(source)
                self.assertIn(source.splitlines()[0], result)

    def test_inline_whisper_and_nested_unrelated_tables(self):
        for source in (
            'whisper = {}\n',
            'engine="local"\nwhisper = {model="tiny", language="ja"}\n',
            'whisper = {mode="local",remote_endpoint="old",remote_model="old",remote_api_key="old",language="en", decoder={threads=2, options=[1,2]}} # comment\n',
            'whisper = { decoder.threads = 2, model = "small" }\n',
        ):
            with self.subTest(source=source):
                result = self.transform(source)
                self.assertNotIn('[whisper]', result)
                if '# comment' in source:
                    self.assertIn('# comment', result)

    def test_multiline_strings_cannot_impersonate_settings(self):
        source = '''engine = "local"
[output]
mode = "paste"
hook = """#!/bin/sh
# [whisper]
engine = 'not a setting'
whisper.mode = "local"
echo \\"escaped quotes\\"
"""
literal = ''' + "'''" + '''
[whisper]
remote_endpoint = 'inside a string'
''' + "'''" + '''
[whisper]
model = "tiny"
language = "es"
mode = """local
multiline old value"""
remote_endpoint = ''' + "'''old\nendpoint'''" + ''' # replace full value
'''
        result = self.transform(source)
        output = source[source.index('[output]'):source.rindex('[whisper]')]
        self.assertIn(output, result)
        self.assertIn('# replace full value', result)

    def test_multiline_quote_runs_and_escaped_delimiters(self):
        for value in ('"""ends with one quote""""', '"""ends with two quotes"""""',
                      "'''ends with one quote''''", "'''ends with two quotes'''''",
                      '"""a \\""" is not the end\nnext line"""'):
            with self.subTest(value=value):
                source = 'hook = ' + value + '\n[whisper]\nmodel="tiny"\n'
                result = self.transform(source)
                self.assertIn('hook = ' + value, result)

    def test_arrays_dates_floats_and_array_tables_preserved(self):
        source = '''preferences = [
  { text = "[whisper]", values = [1, 2, 3] }, # braces and headings
  { text = "engine = 'fake'", values = [] },
]
created = 2026-10-09 12:34:56.123+02:00
local_date = 2026-10-09
local_time = 12:34:56.789
special = [nan, +nan, -nan, inf, -inf, -0.0]
[whisper]
model = "tiny"
[[whisper.presets]]
name = "fast"
settings = {threads = 2}
[[whisper.presets]]
name = "accurate"
[[profiles]]
engine = "do not replace"
[profiles.whisper]
mode = "do not replace"
'''
        result = self.transform(source)
        self.assertIn(source[:source.index('[whisper]')], result)
        self.assertIn(source[source.index('[[whisper.presets]]'):], result)

    def test_replaces_target_values_of_any_shape(self):
        for source in (
            'engine = {name="other"}\nwhisper = { mode={old=true}, remote_endpoint=["old"], model="tiny" }\n',
            'engine.old = true\nwhisper.mode.old = true\nwhisper.remote_endpoint.old = "url"\n',
            '[engine]\nold = true\n[whisper.mode]\nold = true\n[whisper.remote_endpoint]\nold = "url"\n',
            '[[engine]]\nold = true\n[[whisper.mode]]\nold = true\n',
            'whisper = {mode.old=true, model="tiny", remote_endpoint.old="url"}\n',
            'whisper = {mode.old=true, mode.other=false}\n',
            'whisper = {model="tiny", mode.old=true, remote_endpoint.old="url"}\n',
        ):
            with self.subTest(source=source):
                self.transform(source)

    def test_existing_language_is_never_rewritten(self):
        for language in ('"pl"', '""', '["pl", "en"]', '{preferred="pl"}'):
            with self.subTest(language=language):
                source = '[whisper]\nlanguage = ' + language + ' # unchanged\n'
                result = self.transform(source)
                self.assertIn('language = ' + language + ' # unchanged', result)

    def test_crlf_and_missing_final_newline(self):
        result = self.transform('# retained\r\n["whisper"]\r\nmodel = "tiny"')
        self.assertNotIn('\n', result.replace('\r\n', ''))
        self.assertIn('model = "tiny"', result)

    def test_endpoint_is_toml_escaped_not_interpolated(self):
        source = '[output]\nmode="clipboard"\n'
        endpoint = 'http://localhost:8377/path?name="quoted"&text=\\snow\n\t☃'
        result = configure(source, endpoint)
        self.assertEqual(endpoint, tomllib.loads(result)['whisper']['remote_endpoint'])
        self.assertEqual(result, configure(result, endpoint))

    def test_rejects_invalid_toml_or_non_table_whisper(self):
        for source in ('engine =', '[whisper]\n[whisper]\n', 'whisper = "local"\n',
                       'whisper = []\n', '[[whisper]]\nmode="local"\n'):
            with self.subTest(source=source):
                with self.assertRaises(ValueError):
                    configure(source, self.endpoint)


if __name__ == '__main__':
    unittest.main()
