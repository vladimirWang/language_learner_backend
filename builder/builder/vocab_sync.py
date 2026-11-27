"""词库快照：从数据库导出 content/<lang>/vocab.csv，并校验快照与数据库是否一致。

数据库是词库唯一真值；CSV 只是可审阅、可回滚的发布快照。
导出方向固定为 DB → 文件，不做反向自动覆盖。
"""

from __future__ import annotations

import csv
import hashlib
import json
import logging
import os
from datetime import datetime, timezone
from pathlib import Path

from .config import Settings

logger = logging.getLogger(__name__)

# 与 ground_truth._VOCAB_COLUMNS 保持一致，保证导出的 CSV 可被 --import-vocab 原样读回。
_VOCAB_CSV_COLUMNS = [
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

MANIFEST_NAME = "vocab.manifest.json"


def export_csv(
    store,
    settings: Settings,
    langs: list[str],
    out_dir: str | Path | None = None,
    version: str | None = None,
) -> dict:
    """把各语种词表导出为按 lemma 排序的 CSV，并写入快照清单。"""
    root = Path(out_dir) if out_dir else settings.content_dir
    manifest = {
        "version": version or datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S"),
        "exported_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "langs": {},
    }
    for lang in langs:
        rows = store.load_vocab_rows(lang)
        rows.sort(key=lambda row: row["lemma"])
        path = root / lang / "vocab.csv"
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".csv.tmp")
        with tmp.open("w", encoding="utf-8", newline="") as fh:
            writer = csv.DictWriter(fh, fieldnames=_VOCAB_CSV_COLUMNS, extrasaction="ignore")
            writer.writeheader()
            for row in rows:
                writer.writerow({column: row.get(column, "") for column in _VOCAB_CSV_COLUMNS})
        os.replace(tmp, path)
        manifest["langs"][lang] = {
            "rows": len(rows),
            "sha256": _sha256(path),
            "path": _relative(path, root),
        }
        logger.info("vocab export lang=%s rows=%d", lang, len(rows))

    manifest_path = root / MANIFEST_NAME
    tmp = manifest_path.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, manifest_path)
    return manifest


def check(store, settings: Settings, langs: list[str]) -> list[str]:
    """对比数据库与 CSV 快照的 lemma 集合，返回差异描述（空表示一致）。"""
    issues: list[str] = []
    for lang in langs:
        path = settings.content_dir / lang / "vocab.csv"
        if not path.is_file():
            issues.append(f"{lang}: 快照不存在（{path}），先执行 --export-vocab")
            continue
        snapshot = _csv_lemmas(path)
        db = {row["lemma"] for row in store.load_vocab_rows(lang)}
        db_only = sorted(db - snapshot)
        snapshot_only = sorted(snapshot - db)
        if db_only or snapshot_only:
            issues.append(
                f"{lang}: 不一致（库中多 {len(db_only)} 条，快照多 {len(snapshot_only)} 条）"
                f"\n  仅在库中: {_preview(db_only)}"
                f"\n  仅在快照: {_preview(snapshot_only)}"
            )
    return issues


def _csv_lemmas(path: Path) -> set[str]:
    with path.open(encoding="utf-8-sig", newline="") as fh:
        reader = csv.DictReader(fh)
        if not reader.fieldnames or "lemma" not in [f.strip() for f in reader.fieldnames]:
            raise ValueError(f"{path} 缺少 lemma 列")
        return {(row.get("lemma") or "").strip() for row in reader if (row.get("lemma") or "").strip()}


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _relative(path: Path, root: Path) -> str:
    try:
        return str(path.relative_to(root))
    except ValueError:
        return str(path)


def _preview(items: list[str], limit: int = 8) -> str:
    if not items:
        return "-"
    head = ", ".join(items[:limit])
    return head + (f" …（共 {len(items)} 条）" if len(items) > limit else "")
