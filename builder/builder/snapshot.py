"""导出与客户端嵌入式向量引擎无关的快照格式（JSONL + manifest）。

客户端可按选定的引擎（Qdrant Embedded / Milvus Lite / Chroma）在本地加载
vectors.jsonl 并写入各自的集合，避免服务端绑定具体引擎。
"""

from __future__ import annotations

import hashlib
import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .db import Store

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SnapshotResult:
    vectors_path: Path
    manifest_path: Path
    chunk_count: int


def export(store: Store, lang: str, version: str, dest_dir: Path) -> SnapshotResult:
    dest_dir.mkdir(parents=True, exist_ok=True)
    vectors_path = dest_dir / "vectors.jsonl"
    manifest_path = dest_dir / "manifest.json"

    count = 0
    with vectors_path.open("w", encoding="utf-8") as fh:
        for chunk in store.iter_chunks(lang):
            record = {
                "id": _point_id(chunk.source_path, chunk.title),
                "vector": chunk.embedding,
                "payload": {
                    "lang": chunk.lang,
                    "level": chunk.level,
                    "chunk_type": chunk.chunk_type,
                    "lemma": chunk.lemma,
                    "title": chunk.title,
                    "content": chunk.content,
                    "source_path": chunk.source_path,
                    "source_hash": chunk.source_hash,
                    "version": version,
                },
            }
            fh.write(json.dumps(record, ensure_ascii=False) + "\n")
            count += 1

    manifest = {
        "lang": lang,
        "version": version,
        "chunk_count": count,
        "format": "jsonl",
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    logger.info("snapshot exported: %s (%d chunks)", vectors_path, count)
    return SnapshotResult(vectors_path=vectors_path, manifest_path=manifest_path, chunk_count=count)


def _point_id(source_path: str, title: str) -> str:
    return hashlib.sha1(f"{source_path}|{title}".encode()).hexdigest()[:16]
