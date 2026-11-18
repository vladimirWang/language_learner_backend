"""PGG 真题 PDF 解析（一次性任务）。

阶段 1 目标：按页提取文本、识别套卷与题型块、清洗文本，输出可供人工校对的
中间 JSON；真正的结构化入库在后续阶段完成。

用法:
    python -m builder.pdf_import --job-id pdf-xxx --pdf /path/paper.pdf \
        --lang de --out /path/result.json

Go 侧通过逐行 stdout 记录日志，并读取 `RESULT_JSON: {...}` 汇总行。
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

SECTION_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("diktat", re.compile(r"\bdiktat\b", re.IGNORECASE)),
    ("hoeren", re.compile(r"h[oö]rverstehen", re.IGNORECASE)),
    ("lesen", re.compile(r"leseverstehen", re.IGNORECASE)),
    (
        "grammar",
        re.compile(r"grammatik\s*(?:und|&)\s*wortschatz", re.IGNORECASE),
    ),
    ("writing", re.compile(r"schriftlicher\s+ausdruck", re.IGNORECASE)),
]

PAPER_PATTERN = re.compile(r"PGG\s*[-_]?\s*(20\d{2})\s*([AB])?", re.IGNORECASE)


def clean_text(text: str) -> str:
    """去掉页眉页脚式的孤立页码与多余空行，保留德语原文。"""
    lines: list[str] = []
    for raw in text.replace("\r", "\n").split("\n"):
        line = raw.strip()
        if not line:
            continue
        # 纯页码行
        if re.fullmatch(r"[-–—\s]*\d{1,3}[-–—\s]*", line):
            continue
        lines.append(line)
    return "\n".join(lines)


def detect_sections(text: str) -> list[str]:
    return [name for name, pattern in SECTION_PATTERNS if pattern.search(text)]


def detect_paper_id(text: str) -> str | None:
    match = PAPER_PATTERN.search(text)
    if not match:
        return None
    year = match.group(1)
    variant = (match.group(2) or "A").lower()
    return f"pgg_{year}_{variant}"


def parse_pdf(pdf_path: Path, lang: str) -> dict:
    import fitz  # PyMuPDF

    doc = fitz.open(pdf_path)
    blocks: list[dict] = []
    papers: dict[str, dict] = {}
    section_counts: dict[str, int] = {}
    current_paper: str | None = None
    current_section: str | None = None

    total_pages = doc.page_count
    print(f"打开 PDF: {pdf_path.name}，共 {total_pages} 页", flush=True)

    for page_no in range(1, total_pages + 1):
        page = doc.load_page(page_no - 1)
        text = clean_text(page.get_text("text") or "")
        if not text:
            print(f"第 {page_no}/{total_pages} 页无文本（可能是扫描页，需要 OCR）", flush=True)
            continue

        paper_id = detect_paper_id(text) or current_paper
        if paper_id:
            current_paper = paper_id
        sections = detect_sections(text)
        if sections:
            current_section = sections[0]

        section = current_section or "unknown"
        section_counts[section] = section_counts.get(section, 0) + 1

        if current_paper:
            paper = papers.setdefault(
                current_paper,
                {"paper_id": current_paper, "pages": [], "sections": []},
            )
            paper["pages"].append(page_no)
            if section not in paper["sections"]:
                paper["sections"].append(section)

        blocks.append(
            {
                "page_no": page_no,
                "paper_id": current_paper,
                "exam_system": "pgg",
                "lang": lang,
                "section": section,
                "raw_text": text,
            }
        )
        print(
            f"第 {page_no}/{total_pages} 页：{len(text)} 字符，"
            f"套卷={current_paper or '-'}，题型={section}",
            flush=True,
        )

    result = {
        "lang": lang,
        "source": pdf_path.name,
        "page_count": total_pages,
        "papers": list(papers.values()),
        "section_page_counts": section_counts,
        "blocks": blocks,
    }
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description="PGG PDF 试卷解析")
    parser.add_argument("--job-id", required=True)
    parser.add_argument("--pdf", required=True)
    parser.add_argument("--lang", default="de")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    pdf_path = Path(args.pdf)
    if not pdf_path.is_file():
        print(f"ERROR: PDF 不存在: {pdf_path}", file=sys.stderr, flush=True)
        return 2

    try:
        result = parse_pdf(pdf_path, args.lang)
    except Exception as exc:  # noqa: BLE001 - 一次性任务，直接打印根因
        print(f"ERROR: 解析失败: {exc}", file=sys.stderr, flush=True)
        return 1

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(
        json.dumps(result, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    print(f"中间结果已写入 {out_path}", flush=True)

    summary = {
        "job_id": args.job_id,
        "page_count": result["page_count"],
        "block_count": len(result["blocks"]),
        "paper_count": len(result["papers"]),
        "papers": [paper["paper_id"] for paper in result["papers"]],
        "section_page_counts": result["section_page_counts"],
        "result_path": str(out_path),
    }
    print("RESULT_JSON: " + json.dumps(summary, ensure_ascii=False), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
