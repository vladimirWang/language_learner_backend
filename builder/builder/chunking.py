"""切片：Markdown 按二级标题切块，PDF 按页提取。"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path

from markdown_it import MarkdownIt

from .config import BuildError, Settings
from .index import SourceEntry

logger = logging.getLogger(__name__)

_md = MarkdownIt("commonmark")


@dataclass(frozen=True)
class Chunk:
    lang: str
    level: str | None
    chunk_type: str
    lemma: str | None
    title: str
    content: str
    source_path: str
    source_hash: str
    patterns: str = ""
    prompts: str = ""


def chunk_entry(settings: Settings, entry: SourceEntry) -> list[Chunk]:
    path = settings.content_dir / entry.path
    if entry.chunk_type == "exam_paper":
        return _chunk_pdf(path, entry)
    return _chunk_markdown(path, entry)


def _chunk_markdown(path: Path, entry: SourceEntry) -> list[Chunk]:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines()
    headings = _headings(_md.parse(text))
    h2 = [(token, title) for token, title in headings if token.tag == "h2"]
    doc_title = next((title for token, title in headings if token.tag == "h1"), path.stem)
    lemma = path.stem if entry.chunk_type == "vocab" else None

    if not h2:
        body = text.strip()
        return [_make_chunk(entry, doc_title, body, lemma)] if body else []

    preamble = "\n".join(lines[: _start(h2[0][0])]).strip()
    chunks: list[Chunk] = []
    for i, (token, title) in enumerate(h2):
        start = _start(token)
        end = _start(h2[i + 1][0]) if i + 1 < len(h2) else len(lines)
        body = "\n".join(lines[start:end]).strip()
        if i == 0 and preamble:
            body = f"{preamble}\n\n{body}"
        if body:
            chunks.append(_make_chunk(entry, title or doc_title, body, lemma))
    return chunks


def _chunk_pdf(path: Path, entry: SourceEntry) -> list[Chunk]:
    try:
        import fitz
    except ImportError as exc:  # pragma: no cover - 依赖缺失属于环境问题
        raise BuildError("pymupdf is required to parse exam pdf") from exc

    chunks: list[Chunk] = []
    with fitz.open(str(path)) as doc:
        for page_no, page in enumerate(doc, start=1):
            text = page.get_text("text").strip()
            if len(text) < 20:
                continue
            chunks.append(_make_chunk(entry, f"{path.name} p.{page_no}", text, None))
    if not chunks:
        logger.warning("no text extracted from %s", entry.path)
    return chunks


def _make_chunk(entry: SourceEntry, title: str, body: str, lemma: str | None) -> Chunk:
    return Chunk(
        lang=entry.lang,
        level=entry.level,
        chunk_type=entry.chunk_type,
        lemma=lemma,
        title=title,
        content=body,
        source_path=entry.path,
        source_hash=entry.source_hash,
        patterns=entry.patterns,
        prompts=entry.prompts,
    )


def _headings(tokens) -> list[tuple[object, str]]:
    """返回 (heading_open token, 标题文本) 列表。"""
    result: list[tuple[object, str]] = []
    for i, token in enumerate(tokens):
        if token.type != "heading_open":
            continue
        title = tokens[i + 1].content.strip() if i + 1 < len(tokens) else ""
        result.append((token, title))
    return result


def _start(token) -> int:
    return token.map[0] if token.map else 0
