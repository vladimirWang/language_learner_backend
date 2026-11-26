"""CSV 校验 → 生成客户端只读真值库 ground_truth.db（SQLite）。"""

from __future__ import annotations

import csv
import logging
import os
import re
import sqlite3
from dataclasses import dataclass
from pathlib import Path

from .config import BuildError, Settings
from .plugins import get_plugin

logger = logging.getLogger(__name__)

_VOCAB_COLUMNS = [
    "lang",
    "lemma",
    "word_type",
    "gender",
    "plural",
    "separable",
    "trans_intr",
    "level",
    "chinese_mean",
    "english_mean",
    "core_case",
    "sort_key",
    "extra_json",
    "source_md_path",
]

_FORM_COLUMNS = [
    "lang",
    "root",
    "form_type",
    "person",
    "case_tag",
    "gender",
    "number_tag",
    "form",
    "note",
    "source_md_path",
]

_VOCAB_DDL = """
CREATE TABLE vocab (
    word_id        INTEGER PRIMARY KEY AUTOINCREMENT,
    lang           TEXT NOT NULL,
    lemma          TEXT NOT NULL,
    word_type      TEXT NOT NULL,
    gender         TEXT,
    plural         TEXT,
    separable      BOOLEAN,
    trans_intr     TEXT,
    level          TEXT NOT NULL,
    chinese_mean   TEXT NOT NULL,
    english_mean   TEXT,
    core_case      TEXT,
    sort_key       TEXT,
    extra_json     TEXT,
    source_md_path TEXT,
    create_version TEXT,
    UNIQUE(lang, lemma)
)
"""

_FORMS_DDL = """
CREATE TABLE grammar_forms (
    form_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    lang       TEXT NOT NULL,
    root       TEXT NOT NULL,
    form_type  TEXT NOT NULL,
    person     TEXT,
    case_tag   TEXT,
    gender     TEXT,
    number_tag TEXT,
    form       TEXT NOT NULL,
    note       TEXT,
    source_md_path TEXT,
    create_version TEXT,
    UNIQUE(lang, root, form_type, person, case_tag, gender, number_tag)
)
"""


@dataclass(frozen=True)
class GroundTruthResult:
    path: Path
    vocab_count: int
    form_count: int
    vocab_rows: list[dict[str, str]]


def load_vocab(settings: Settings, lang: str) -> list[dict[str, str]]:
    """读取并校验 vocab.csv，返回带例句的规范化行；供真值库与 PG 词表共用。"""
    plugin = get_plugin(lang)
    vocab_path = settings.content_dir / lang / "vocab.csv"
    if not vocab_path.is_file():
        raise BuildError(f"vocab.csv not found for lang={lang}: {vocab_path}")

    rows = _read_csv(vocab_path, _VOCAB_COLUMNS)
    errors: list[str] = []
    seen: set[tuple[str, str]] = set()
    for line_no, row in rows:
        errors.extend(plugin.validate_vocab(row, f"vocab.csv:{line_no}"))
        key = (row["lang"], row["lemma"])
        if key in seen:
            errors.append(f"vocab.csv:{line_no}: duplicate lemma {row['lemma']}")
        seen.add(key)
        _warn_missing_md(settings, row.get("source_md_path", ""), f"vocab.csv:{line_no}")
    if errors:
        detail = "\n".join(errors[:20])
        raise BuildError(f"content validation failed for lang={lang}:\n{detail}")

    vocab: list[dict[str, str]] = []
    for _, row in rows:
        item = dict(row)
        item[f"example_{lang}"] = _first_example(settings, item.get("source_md_path", ""))
        vocab.append(item)
    return vocab


def build(settings: Settings, lang: str, version: str) -> GroundTruthResult:
    plugin = get_plugin(lang)
    lang_dir = settings.content_dir / lang

    vocab_rows = load_vocab(settings, lang)

    errors: list[str] = []
    form_rows: list[tuple[int, dict[str, str]]] = []
    form_path = lang_dir / "grammar_forms.csv"
    if form_path.is_file():
        form_rows = _read_csv(form_path, _FORM_COLUMNS)
        seen_form: set[tuple[str, ...]] = set()
        for line_no, row in form_rows:
            errors.extend(plugin.validate_form(row, f"grammar_forms.csv:{line_no}"))
            key = tuple(row[column] for column in _FORM_COLUMNS[:8])
            if key in seen_form:
                errors.append(f"grammar_forms.csv:{line_no}: duplicate form {row.get('root')} {row.get('form')}")
            seen_form.add(key)
            _warn_missing_md(settings, row.get("source_md_path", ""), f"grammar_forms.csv:{line_no}")
    else:
        logger.info("no grammar_forms.csv for lang=%s, skipping forms", lang)

    if errors:
        detail = "\n".join(errors[:20])
        raise BuildError(f"content validation failed for lang={lang}:\n{detail}")

    dest = settings.work_dir / version / lang / "ground_truth.db"
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_suffix(".db.tmp")
    if tmp.exists():
        tmp.unlink()

    conn = sqlite3.connect(tmp)
    try:
        conn.execute(_VOCAB_DDL)
        conn.execute(_FORMS_DDL)
        conn.executemany(
            """
            INSERT INTO vocab (lang, lemma, word_type, gender, plural, separable, trans_intr,
                               level, chinese_mean, english_mean, core_case, sort_key,
                               extra_json, source_md_path, create_version)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    row["lang"],
                    row["lemma"],
                    row["word_type"],
                    _blank_none(row["gender"]),
                    _blank_none(row["plural"]),
                    _optional_bool(row["separable"]),
                    _blank_none(row["trans_intr"]),
                    row["level"],
                    row["chinese_mean"],
                    _blank_none(row["english_mean"]),
                    _blank_none(row["core_case"]),
                    _blank_none(row["sort_key"]),
                    _blank_none(row["extra_json"]),
                    _blank_none(row["source_md_path"]),
                    version,
                )
                for row in vocab_rows
            ],
        )
        conn.executemany(
            """
            INSERT INTO grammar_forms (lang, root, form_type, person, case_tag, gender,
                                       number_tag, form, note, source_md_path, create_version)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    row["lang"],
                    row["root"],
                    row["form_type"],
                    _blank_none(row["person"]),
                    _blank_none(row["case_tag"]),
                    _blank_none(row["gender"]),
                    _blank_none(row["number_tag"]),
                    row["form"],
                    _blank_none(row["note"]),
                    _blank_none(row["source_md_path"]),
                    version,
                )
                for _, row in form_rows
            ],
        )
        conn.execute("CREATE INDEX idx_vocab_lang_lemma ON vocab (lang, lemma)")
        conn.execute("CREATE INDEX idx_vocab_lang_type ON vocab (lang, word_type)")
        conn.execute("CREATE INDEX idx_forms_lang_root ON grammar_forms (lang, root)")
        conn.commit()
    finally:
        conn.close()

    os.replace(tmp, dest)
    logger.info("ground truth built: %s (vocab=%d, forms=%d)", dest, len(vocab_rows), len(form_rows))
    return GroundTruthResult(
        path=dest,
        vocab_count=len(vocab_rows),
        form_count=len(form_rows),
        vocab_rows=vocab_rows,
    )


def _read_csv(path: Path, required: list[str]) -> list[tuple[int, dict[str, str]]]:
    with path.open(encoding="utf-8-sig", newline="") as fh:
        reader = csv.DictReader(fh)
        header = [(name or "").strip() for name in (reader.fieldnames or [])]
        missing = [column for column in required if column not in header]
        if missing:
            raise BuildError(f"{path.name} missing columns: {', '.join(missing)}")
        rows: list[tuple[int, dict[str, str]]] = []
        for line_no, raw in enumerate(reader, start=2):
            if not any((value or "").strip() for value in raw.values()):
                continue
            rows.append((line_no, {key: (value or "").strip() for key, value in raw.items() if key}))
    return rows


def _warn_missing_md(settings: Settings, rel_path: str, where: str) -> None:
    if rel_path and not (settings.content_dir / rel_path).is_file():
        logger.warning("%s: source_md_path not found: %s", where, rel_path)


_CJK = re.compile(r"[\u3000-\u303f\u3400-\u4dbf\u4e00-\u9fff]")


def _first_example(settings: Settings, rel_path: str) -> str:
    """取词条 md 中 `## 例句` 下的第一条德语例句；中文解释从首个 CJK 字符处截断。"""
    if not rel_path:
        return ""
    path = settings.content_dir / rel_path
    if not path.is_file():
        return ""
    in_section = False
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if line.startswith("## "):
            in_section = line[3:].strip() == "例句"
            continue
        if in_section and line.startswith("- "):
            text = line[2:].strip()
            match = _CJK.search(text)
            if match:
                text = text[: match.start()].strip()
            return text
    return ""


def _blank_none(value: str) -> str | None:
    return value or None


def _optional_bool(value: str) -> int | None:
    if not value:
        return None
    return 1 if value.lower() in {"true", "1", "yes"} else 0
