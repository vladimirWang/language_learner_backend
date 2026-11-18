"""PGG 真题 PDF 解析（一次性任务）。

流程：
1. 按页提取文字；扫描页用 tesseract OCR 兜底。
2. 识别套卷与题型块，清理文本，输出可人工校对的中间 JSON。
3. 从文本块抽取五类题型（听写 / 听力 / 阅读 / 语法词汇 / 写作），生成
   `pgg_exam.db`（SQLite 真值库）供判分与离线分发。

用法:
    python -m builder.pdf_import --job-id pdf-xxx --pdf /path/paper.pdf \
        --lang de --out /path/result.json

Go 侧通过逐行 stdout 记录日志，并读取 `RESULT_JSON: {...}` 汇总行。
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
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

# 扫描页 OCR：文字量低于阈值且页面有图片时触发。
OCR_MIN_CHARS = 40
OCR_DPI = 200
OCR_TIMEOUT_SEC = 180
# tesseract 语言代码与项目 lang 的映射。
OCR_LANG_MAP = {"de": "deu", "en": "eng", "ja": "jpn", "zh": "chi_sim"}

ANSWER_MARKER = re.compile(
    r"(l[oö]sungsschl[üu]ssel|antwortschl[üu]ssel|参考答案|答案|听力原文|h[oö]rtext|transkript)",
    re.IGNORECASE,
)
QUESTION_NO = re.compile(r"(?m)^\s*(\d{1,3})\s*[.)]\s+")
# 选项可能大写也可能小写，且多个选项可能挤在同一行，因此不限定行首。
OPTION_MARK = re.compile(r"(?m)(?:^|\s)([A-Da-d])\s*[).]\s+")
ANSWER_PAIR = re.compile(r"(\d{1,3})\s*[.)\-:：]?\s*([A-D])\b")
WORD_LIMIT = re.compile(r"(\d{2,3})\s*(?:w[oö]rter|worte|words)", re.IGNORECASE)

SQLITE_SCHEMA = """
CREATE TABLE exam_papers (
    paper_id       TEXT PRIMARY KEY,
    exam_system    TEXT NOT NULL,
    level          TEXT,
    year           INTEGER,
    title          TEXT,
    source_note    TEXT,
    create_version TEXT
);
CREATE TABLE exam_questions (
    question_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id       TEXT NOT NULL,
    exam_system    TEXT NOT NULL,
    section        TEXT NOT NULL,
    q_no           TEXT,
    stem           TEXT NOT NULL,
    options_json   TEXT,
    correct_answer TEXT,
    source_text_id TEXT,
    grammar_ref    TEXT,
    level          TEXT,
    create_version TEXT
);
CREATE TABLE exam_diktat (
    diktat_id      INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id       TEXT NOT NULL,
    full_text      TEXT NOT NULL,
    word_count     INTEGER,
    audio_ref      TEXT,
    create_version TEXT
);
CREATE TABLE exam_writing (
    writing_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id       TEXT NOT NULL,
    prompt         TEXT NOT NULL,
    word_limit     INTEGER,
    material_ref   TEXT,
    create_version TEXT
);
"""


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


def ocr_page(page, lang: str) -> str:
    """把页面渲染成图片后交给 tesseract 识别；不可用时返回空串。"""
    if not shutil.which("tesseract"):
        return ""
    tess_lang = OCR_LANG_MAP.get(lang, lang)
    pix = page.get_pixmap(dpi=OCR_DPI)
    with tempfile.NamedTemporaryFile(suffix=".png") as image_file:
        pix.save(image_file.name)
        try:
            proc = subprocess.run(
                ["tesseract", image_file.name, "-", "-l", tess_lang, "--psm", "6"],
                capture_output=True,
                text=True,
                timeout=OCR_TIMEOUT_SEC,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            return ""
    if proc.returncode != 0:
        return ""
    return proc.stdout or ""


def parse_pdf(
    pdf_path: Path, lang: str, enable_ocr: bool = True, max_pages: int = 0
) -> dict:
    import fitz  # PyMuPDF

    doc = fitz.open(pdf_path)
    blocks: list[dict] = []
    papers: dict[str, dict] = {}
    section_counts: dict[str, int] = {}
    current_paper: str | None = None
    current_section: str | None = None
    ocr_pages: list[int] = []

    total_pages = doc.page_count
    limit_pages = min(total_pages, max_pages) if max_pages > 0 else total_pages
    print(f"打开 PDF: {pdf_path.name}，共 {total_pages} 页", flush=True)
    if limit_pages < total_pages:
        print(f"调试模式：仅解析前 {limit_pages} 页", flush=True)

    for page_no in range(1, limit_pages + 1):
        page = doc.load_page(page_no - 1)
        text = clean_text(page.get_text("text") or "")
        ocr_used = False
        if len(text) < OCR_MIN_CHARS and enable_ocr:
            print(f"第 {page_no}/{total_pages} 页文字过少，开始 OCR…", flush=True)
            ocr_text = clean_text(ocr_page(page, lang))
            if len(ocr_text) > len(text):
                text = ocr_text
                ocr_used = True
                ocr_pages.append(page_no)
                print(f"第 {page_no}/{total_pages} 页 OCR 完成：{len(text)} 字符", flush=True)
        if not text:
            print(f"第 {page_no}/{total_pages} 页无文本（OCR 不可用或失败）", flush=True)
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
                "ocr": ocr_used,
            }
        )
        print(
            f"第 {page_no}/{total_pages} 页：{len(text)} 字符，"
            f"套卷={current_paper or '-'}，题型={section}"
            f"{'，OCR' if ocr_used else ''}",
            flush=True,
        )

    result = {
        "lang": lang,
        "source": pdf_path.name,
        "page_count": total_pages,
        "ocr_pages": ocr_pages,
        "papers": list(papers.values()),
        "section_page_counts": section_counts,
        "blocks": blocks,
    }
    return result


def split_questions(text: str) -> list[dict]:
    """按题号切分题块，并解析 A/B/C/D 选项。"""
    markers = list(QUESTION_NO.finditer(text))
    questions: list[dict] = []
    for index, marker in enumerate(markers):
        end = markers[index + 1].start() if index + 1 < len(markers) else len(text)
        chunk = text[marker.end() : end]
        option_marks = list(OPTION_MARK.finditer(chunk))
        if len(option_marks) < 2:
            continue
        stem = chunk[: option_marks[0].start()].strip()
        options: list[str] = []
        for opt_index, opt in enumerate(option_marks):
            opt_end = (
                option_marks[opt_index + 1].start()
                if opt_index + 1 < len(option_marks)
                else len(chunk)
            )
            body = chunk[opt.end() : opt_end].strip().replace("\n", " ")
            options.append(f"{opt.group(1).upper()}. {body}")
        questions.append(
            {
                "q_no": marker.group(1),
                "stem": stem.replace("\n", " ").strip(),
                "options": options,
            }
        )
    return questions


def parse_answers(text: str) -> dict[str, str]:
    answers: dict[str, str] = {}
    for q_no, letter in ANSWER_PAIR.findall(text):
        answers.setdefault(q_no, letter.upper())
    return answers


def extract_exam(result: dict, version: str) -> dict:
    """从校对块抽取结构化试题（阶段 2）。"""
    papers: dict[str, dict] = {}
    questions: list[dict] = []
    diktat: list[dict] = []
    writing: list[dict] = []

    for block in result.get("blocks", []):
        paper_id = block.get("paper_id")
        if not paper_id:
            continue
        year_match = re.search(r"(20\d{2})", paper_id)
        papers.setdefault(
            paper_id,
            {
                "paper_id": paper_id,
                "exam_system": "pgg",
                "level": "pgg4",
                "year": int(year_match.group(1)) if year_match else None,
                "title": paper_id.replace("_", " ").upper(),
                "source_note": "PGG 真题，仅限个人学习使用",
            },
        )

    # 先跑题目，再按答案块补充 correct_answer。
    answers_by_paper: dict[str, dict[str, str]] = {}
    for block in result.get("blocks", []):
        paper_id = block.get("paper_id")
        text = block.get("raw_text") or ""
        if not paper_id:
            continue
        if ANSWER_MARKER.search(text):
            answers_by_paper.setdefault(paper_id, {}).update(parse_answers(text))

    for block in result.get("blocks", []):
        paper_id = block.get("paper_id")
        text = block.get("raw_text") or ""
        section = block.get("section") or "unknown"
        if not paper_id or ANSWER_MARKER.search(text):
            continue

        if section in {"hoeren", "lesen", "grammar"}:
            for item in split_questions(text):
                questions.append(
                    {
                        "paper_id": paper_id,
                        "exam_system": "pgg",
                        "section": section,
                        "q_no": item["q_no"],
                        "stem": item["stem"],
                        "options": item["options"],
                        "correct_answer": answers_by_paper.get(paper_id, {}).get(item["q_no"]),
                        "level": "pgg4",
                    }
                )
        elif section == "diktat":
            full_text = text.strip()
            if len(full_text) > 40:
                diktat.append(
                    {
                        "paper_id": paper_id,
                        "full_text": full_text,
                        "word_count": len(full_text.split()),
                    }
                )
        elif section == "writing":
            limit_match = WORD_LIMIT.search(text)
            prompt = text.strip()
            if prompt:
                writing.append(
                    {
                        "paper_id": paper_id,
                        "prompt": prompt,
                        "word_limit": int(limit_match.group(1)) if limit_match else None,
                    }
                )

    # 题号去重，保留首次出现（同一套卷同一题型）。
    seen: set[tuple[str, str, str]] = set()
    unique_questions: list[dict] = []
    for question in questions:
        key = (question["paper_id"], question["section"], question["q_no"])
        if key in seen:
            continue
        seen.add(key)
        unique_questions.append(question)

    return {
        "version": version,
        "create_version": version,
        "papers": list(papers.values()),
        "questions": unique_questions,
        "diktat": diktat,
        "writing": writing,
    }


def write_sqlite(exam: dict, db_path: Path) -> None:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    if db_path.exists():
        db_path.unlink()
    conn = sqlite3.connect(db_path)
    try:
        conn.executescript(SQLITE_SCHEMA)
        version = exam.get("create_version") or "pgg-import"
        conn.executemany(
            "INSERT INTO exam_papers (paper_id, exam_system, level, year, title, source_note, create_version)"
            " VALUES (?, ?, ?, ?, ?, ?, ?)",
            [
                (
                    paper["paper_id"],
                    paper["exam_system"],
                    paper["level"],
                    paper["year"],
                    paper["title"],
                    paper["source_note"],
                    version,
                )
                for paper in exam["papers"]
            ],
        )
        conn.executemany(
            "INSERT INTO exam_questions (paper_id, exam_system, section, q_no, stem, options_json,"
            " correct_answer, level, create_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            [
                (
                    question["paper_id"],
                    question["exam_system"],
                    question["section"],
                    question["q_no"],
                    question["stem"],
                    json.dumps(question["options"], ensure_ascii=False),
                    question["correct_answer"],
                    question["level"],
                    version,
                )
                for question in exam["questions"]
            ],
        )
        conn.executemany(
            "INSERT INTO exam_diktat (paper_id, full_text, word_count, create_version)"
            " VALUES (?, ?, ?, ?)",
            [
                (item["paper_id"], item["full_text"], item["word_count"], version)
                for item in exam["diktat"]
            ],
        )
        conn.executemany(
            "INSERT INTO exam_writing (paper_id, prompt, word_limit, create_version)"
            " VALUES (?, ?, ?, ?)",
            [
                (item["paper_id"], item["prompt"], item["word_limit"], version)
                for item in exam["writing"]
            ],
        )
        conn.commit()
    finally:
        conn.close()


def main() -> int:
    parser = argparse.ArgumentParser(description="PGG PDF 试卷解析")
    parser.add_argument("--job-id", required=True)
    parser.add_argument("--pdf", required=True)
    parser.add_argument("--lang", default="de")
    parser.add_argument("--out", required=True)
    parser.add_argument("--no-ocr", action="store_true", help="禁用扫描页 OCR")
    parser.add_argument("--max-pages", type=int, default=0, help="仅解析前 N 页（调试用）")
    args = parser.parse_args()

    pdf_path = Path(args.pdf)
    if not pdf_path.is_file():
        print(f"ERROR: PDF 不存在: {pdf_path}", file=sys.stderr, flush=True)
        return 2

    try:
        result = parse_pdf(
            pdf_path, args.lang, enable_ocr=not args.no_ocr, max_pages=args.max_pages
        )
    except Exception as exc:  # noqa: BLE001 - 一次性任务，直接打印根因
        print(f"ERROR: 解析失败: {exc}", file=sys.stderr, flush=True)
        return 1

    if args.max_pages > 0:
        result["blocks"] = [b for b in result["blocks"] if b["page_no"] <= args.max_pages]
        result["page_count"] = min(result["page_count"], args.max_pages)

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)

    try:
        exam = extract_exam(result, version=args.job_id)
        db_path = out_path.parent / "pgg_exam.db"
        write_sqlite(exam, db_path)
        print(
            f"结构化完成：{len(exam['papers'])} 套卷、{len(exam['questions'])} 道客观题、"
            f"{len(exam['diktat'])} 篇听写、{len(exam['writing'])} 篇写作",
            flush=True,
        )
        result["exam"] = exam
        result["exam_db_path"] = str(db_path)
    except Exception as exc:  # noqa: BLE001 - 结构化失败不阻塞校对 JSON
        print(f"WARN: 结构化失败: {exc}", flush=True)

    out_path.write_text(
        json.dumps(result, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    print(f"中间结果已写入 {out_path}", flush=True)

    exam = result.get("exam") or {"papers": [], "questions": [], "diktat": [], "writing": []}
    summary = {
        "job_id": args.job_id,
        "page_count": result["page_count"],
        "block_count": len(result["blocks"]),
        "paper_count": len(result["papers"]),
        "papers": [paper["paper_id"] for paper in result["papers"]],
        "section_page_counts": result["section_page_counts"],
        "ocr_page_count": len(result.get("ocr_pages") or []),
        "question_count": len(exam["questions"]),
        "diktat_count": len(exam["diktat"]),
        "writing_count": len(exam["writing"]),
        "exam_db_path": result.get("exam_db_path"),
        "result_path": str(out_path),
    }
    print("RESULT_JSON: " + json.dumps(summary, ensure_ascii=False), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
