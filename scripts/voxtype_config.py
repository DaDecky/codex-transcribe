"""Surgically select Voxtype's remote Whisper backend (Python 3.11+)."""

from __future__ import annotations

import copy
import json
import math
import tomllib
from dataclasses import dataclass, field


@dataclass
class _Token:
    text: str
    start: int
    end: int


@dataclass
class _Entry:
    path: tuple[str, ...]
    start: int
    value_start: int
    end: int
    children: list[_Entry] = field(default_factory=list)
    close: int | None = None


@dataclass
class _Header:
    path: tuple[str, ...]
    start: int
    end: int
    insertion: int
    array: bool


def _tokens(text: str) -> list[_Token]:
    """Skip trivia, but keep newlines and complete string tokens opaque."""
    result = []
    i = 0
    while i < len(text):
        start = i
        char = text[i]
        if char in " \t\r":
            i += 1
            continue
        if char == "#":
            end = text.find("\n", i)
            i = len(text) if end < 0 else end
            continue
        if char in "\"'":
            triple = text.startswith(char * 3, i)
            delimiter = char * (3 if triple else 1)
            i += len(delimiter)
            while i < len(text):
                if char == '"' and text[i] == "\\":
                    i += 2
                elif text.startswith(delimiter, i):
                    i += len(delimiter)
                    if triple:
                        # Four/five quotes end a multiline string with literal quotes.
                        for _ in range(2):
                            if i < len(text) and text[i] == char:
                                i += 1
                            else:
                                break
                    break
                else:
                    i += 1
        elif char in "\n[]{}=,.":
            i += 1
        else:
            while i < len(text) and text[i] not in " \t\r\n[]{}=,.#\"'":
                i += 1
        result.append(_Token(text[start:i], start, i))
    return result


def _key(text: str) -> tuple[str, ...]:
    value = tomllib.loads(text + " = 0")
    path = []
    while isinstance(value, dict):
        name, value = next(iter(value.items()))
        path.append(name)
    return tuple(path)


def _locate(text: str) -> tuple[list[_Entry], list[_Header]]:
    tokens = _tokens(text)
    entries = []
    headers = []
    context: tuple[str, ...] = ()

    def assignment(i: int, prefix: tuple[str, ...], inline: bool) -> tuple[_Entry, int]:
        start = tokens[i].start
        key_start = i
        while tokens[i].text != "=":
            i += 1
        path = prefix + _key(text[tokens[key_start].start:tokens[i].start])
        i += 1
        value_start = i
        depth = 0
        while i < len(tokens):
            token = tokens[i].text
            if depth == 0 and (token in (",", "}") if inline else token == "\n"):
                break
            if token in ("[", "{"):
                depth += 1
            elif token in ("]", "}"):
                depth -= 1
            i += 1
        entry = _Entry(path, start, tokens[value_start].start, tokens[i - 1].end)
        if tokens[value_start].text == "{":
            entry.close = tokens[i - 1].start
            child = value_start + 1
            while child < i - 1:
                nested, child = assignment(child, path, True)
                entry.children.append(nested)
                if tokens[child].text == ",":
                    child += 1
        return entry, i

    i = 0
    while i < len(tokens):
        if tokens[i].text == "\n":
            i += 1
            continue
        if tokens[i].text == "[":
            start = tokens[i].start
            array = i + 1 < len(tokens) and tokens[i + 1].text == "["
            i += 2 if array else 1
            key_start = i
            while tokens[i].text != "]":
                i += 1
            context = _key(text[tokens[key_start].start:tokens[i].start])
            i += 2 if array else 1
            end = tokens[i - 1].end
            newline = text.find("\n", end)
            insertion = len(text) if newline < 0 else newline + 1
            headers.append(_Header(context, start, end, insertion, array))
        else:
            entry, i = assignment(i, context, False)
            entries.append(entry)
    return entries, headers


def _same(left: object, right: object) -> bool:
    """TOML's nan is a semantic value, even though nan != nan."""
    if type(left) is not type(right):
        return False
    if isinstance(left, dict):
        return left.keys() == right.keys() and all(_same(left[k], right[k]) for k in left)
    if isinstance(left, list):
        return len(left) == len(right) and all(_same(a, b) for a, b in zip(left, right))
    if isinstance(left, float) and math.isnan(left):
        return math.isnan(right)
    return left == right


def configure(text: str, endpoint: str) -> str:
    """Return validated TOML, changing only the documented backend settings.

    Malformed TOML or a non-table `whisper` value raises ValueError without
    modifying anything. File IO and transactional backups belong to the caller.
    """
    try:
        original = tomllib.loads(text)
    except tomllib.TOMLDecodeError as error:
        raise ValueError(f"Voxtype config is invalid TOML; fix it before installing: {error}") from error
    if "whisper" in original and not isinstance(original["whisper"], dict):
        raise ValueError("Voxtype 'whisper' must be a TOML table; replace its scalar/array value with a table before installing")

    values = {
        ("engine",): "whisper",
        ("whisper", "mode"): "remote",
        ("whisper", "remote_endpoint"): endpoint,
        ("whisper", "remote_model"): "whisper-1",
        ("whisper", "remote_api_key"): "local-placeholder",
    }
    if "language" not in original.get("whisper", {}):
        values[("whisper", "language")] = "auto"
    expected = copy.deepcopy(original)
    expected["engine"] = "whisper"
    expected.setdefault("whisper", {})
    for path, value in values.items():
        if len(path) == 2:
            expected["whisper"][path[1]] = value

    entries, headers = _locate(text)
    pending = dict(values)
    edits: list[tuple[int, int, str]] = []
    inline_whisper = None
    quote = lambda value: json.dumps(value, ensure_ascii=False)

    def removed(path: tuple[str, ...]) -> bool:
        return any(path[:len(target)] == target for target in values)

    def visit(items: list[_Entry], inline: bool = False) -> None:
        nonlocal inline_whisper
        discarded = []
        for index, entry in enumerate(items):
            if entry.path in values:
                edits.append((entry.value_start, entry.end, quote(values[entry.path])))
                pending.pop(entry.path, None)
            elif removed(entry.path):
                discarded.append(index)
            elif entry.close is not None:
                if entry.path == ("whisper",):
                    inline_whisper = entry
                visit(entry.children, True)
        if not inline:
            for index in discarded:
                entry = items[index]
                edits.append((entry.start, entry.end, ""))
            return
        # Remove whole runs and one adjacent comma, never unrelated inline keys.
        runs = []
        for index in discarded:
            if runs and index == runs[-1][1] + 1:
                runs[-1] = (runs[-1][0], index)
            else:
                runs.append((index, index))
        for first, last in runs:
            if last < len(items) - 1:
                edits.append((items[first].start, items[last + 1].start, ""))
            elif first:
                comma = text.find(",", items[first - 1].end, items[first].start)
                edits.append((comma, items[last].end, ""))
            else:
                edits.append((items[first].start, items[last].end, ""))

    visit(entries)
    for header in headers:
        if removed(header.path):
            edits.append((header.start, header.end, ""))

    newline = "\r\n" if "\r\n" in text else "\n"

    def lines(position: int, additions: list[str]) -> None:
        if additions:
            prefix = newline if position and text[position - 1] != "\n" else ""
            edits.append((position, position, prefix + newline.join(additions) + newline))

    whisper_additions = {path: value for path, value in pending.items() if len(path) == 2}
    if inline_whisper is not None:
        remaining = any(not removed(entry.path) or entry.path in values for entry in inline_whisper.children)
        if whisper_additions:
            addition = (", " if remaining else "") + ", ".join(
                f"{path[1]} = {quote(value)}" for path, value in whisper_additions.items()
            )
            edits.append((inline_whisper.close, inline_whisper.close, addition))
    else:
        whisper_header = next((header for header in headers if header.path == ("whisper",) and not header.array), None)
        if whisper_header is not None:
            lines(whisper_header.insertion, [f"{path[1]} = {quote(value)}" for path, value in whisper_additions.items()])
        else:
            # Root dotted keys also work for implicit parents of existing subtables.
            root_position = text.rfind("\n", 0, headers[0].start) + 1 if headers else len(text)
            lines(root_position, [f"whisper.{path[1]} = {quote(value)}" for path, value in whisper_additions.items()])
    if ("engine",) in pending:
        root_position = text.rfind("\n", 0, headers[0].start) + 1 if headers else len(text)
        lines(root_position, [f"engine = {quote(pending[('engine',)])}"])

    result = text
    # At a shared insertion point, apply additions in reverse so their order is stable.
    for start, end, replacement in sorted(edits, key=lambda edit: (edit[0], edit[1]), reverse=True):
        result = result[:start] + replacement + result[end:]
    try:
        actual = tomllib.loads(result)
    except tomllib.TOMLDecodeError as error:
        raise ValueError(f"Cannot safely update Voxtype TOML; use ordinary root engine and [whisper] settings: {error}") from error
    if not _same(expected, actual):
        raise ValueError("Cannot safely update Voxtype TOML without changing unrelated values; use ordinary root engine and [whisper] settings")
    return result
