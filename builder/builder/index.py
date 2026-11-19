"""素材清单解析与增量比对：grammar.index.md + PDF 真题扫描。"""

from __future__ import annotations

import hashlib
import logging
from dataclasses import dataclass
from pathlib import Path

from .config import BuildError, Settings

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SourceEntry:
    lang: str
    path: str  # 相对 content 根目录的 posix 路径
    level: str | None
    tags: tuple[str, ...]
    patterns: str  # 逗号分隔的正则，供口语语法要求判定
    prompts: str  # 竖线分隔的引导问句，供语法要求定向补答
    source_hash: str
    chunk_type: str


def hash_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            digest.update(block)
    return "sha256:" + digest.hexdigest()


def load_index(settings: Settings) -> list[SourceEntry]:
    """解析 grammar.index.md，校验文件存在并按实际内容重算 hash。"""
    index_path = settings.content_dir / "grammar.index.md"
    if not index_path.is_file():
        raise BuildError(f"index file not found: {index_path}")

    entries: list[SourceEntry] = []
    seen: set[tuple[str, str]] = set()
    for line_no, raw in enumerate(index_path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line.startswith("- ") or "lang:" not in line:
            continue

        fields = _parse_fields(line)
        lang = fields.get("lang", "")
        rel_path = fields.get("path", "")
        if not lang or not rel_path:
            raise BuildError(f"grammar.index.md:{line_no}: lang/path required")

        file_path = settings.content_dir / rel_path
        if not file_path.is_file():
            raise BuildError(f"grammar.index.md:{line_no}: file not found {rel_path}")

        key = (lang, rel_path)
        if key in seen:
            raise BuildError(f"grammar.index.md:{line_no}: duplicate entry {rel_path}")
        seen.add(key)

        actual_hash = hash_file(file_path)
        declared = fields.get("hash", "")
        if declared and declared != actual_hash:
            logger.warning("index hash mismatch for %s, using actual content hash", rel_path)

        patterns = fields.get("patterns", "").strip()
        prompts = fields.get("prompts", "").strip()
        # patterns/prompts 写在索引元数据里，参与增量 hash，否则修改规则不会触发重建。
        source_hash = actual_hash
        if patterns or prompts:
            digest = hashlib.sha256(
                f"{actual_hash}\n{patterns}\n{prompts}".encode("utf-8")
            ).hexdigest()
            source_hash = "sha256:" + digest

        entries.append(
            SourceEntry(
                lang=lang,
                path=rel_path,
                level=fields.get("level") or None,
                tags=tuple(t for t in fields.get("tags", "").split(",") if t),
                patterns=patterns,
                prompts=prompts,
                source_hash=source_hash,
                chunk_type=_chunk_type(rel_path),
            )
        )
    return entries


def load_pdfs(settings: Settings, lang: str) -> list[SourceEntry]:
    """扫描 content/exams/<lang>/*.pdf，按整文件 hash 做增量。"""
    exam_dir = settings.content_dir / "exams" / lang
    if not exam_dir.is_dir():
        return []
    entries: list[SourceEntry] = []
    for pdf in sorted(exam_dir.rglob("*.pdf")):
        rel_path = pdf.relative_to(settings.content_dir).as_posix()
        entries.append(
            SourceEntry(
                lang=lang,
                path=rel_path,
                level=None,
                tags=("exam",),
                patterns="",
                prompts="",
                source_hash=hash_file(pdf),
                chunk_type="exam_paper",
            )
        )
    return entries


def desired_sources(settings: Settings, lang: str) -> list[SourceEntry]:
    entries = [e for e in load_index(settings) if e.lang == lang]
    entries.extend(load_pdfs(settings, lang))
    return entries


def _chunk_type(rel_path: str) -> str:
    parts = rel_path.split("/")
    if len(parts) >= 3 and parts[1] in {"vocab", "grammar"}:
        return parts[1]
    return "grammar"


def _parse_fields(line: str) -> dict[str, str]:
    body = line[2:] if line.startswith("- ") else line
    fields: dict[str, str] = {}
    last_key: str | None = None
    for part in body.split(","):
        token = part.strip()
        if not token:
            continue
        if ":" in token:
            key, value = token.split(":", 1)
            last_key = key.strip()
            fields[last_key] = value.strip()
        elif last_key:
            fields[last_key] = f"{fields[last_key]},{token}"
    return fields
