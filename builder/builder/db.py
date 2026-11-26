"""构建期 PostgreSQL / pgvector 访问：chunk 入库、增量比对、版本与日志。"""

from __future__ import annotations

import json
import logging
from collections.abc import Iterator
from dataclasses import dataclass

import psycopg
from pgvector.psycopg import register_vector

from .chunking import Chunk

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class StoredChunk:
    chunk_id: int
    lang: str
    level: str | None
    chunk_type: str
    lemma: str | None
    title: str
    content: str
    source_path: str
    source_hash: str
    embedding: list[float]


class Store:
    def __init__(self, dsn: str) -> None:
        self._conn = psycopg.connect(dsn, connect_timeout=10)
        # vector 扩展与表结构由 golang-migrate 在 builder 启动前创建。
        register_vector(self._conn)

    def close(self) -> None:
        self._conn.close()

    def create_vector_index(self) -> None:
        """数据写入后建 HNSW，避免批量写入期 maintenance_work_mem 尖峰。"""
        with self._conn.cursor() as cur:
            cur.execute(
                """
                CREATE INDEX IF NOT EXISTS idx_rag_chunks_embedding
                    ON rag_chunks USING hnsw (embedding vector_cosine_ops)
                    WITH (m=16, ef_construction=64)
                """
            )
        self._conn.commit()

    def source_hashes(self, lang: str) -> dict[str, str]:
        with self._conn.cursor() as cur:
            cur.execute(
                "SELECT DISTINCT source_path, source_hash FROM rag_chunks WHERE lang = %s",
                (lang,),
            )
            return {path: sha for path, sha in cur.fetchall()}

    def replace_chunks(
        self, delete_paths: set[str], chunks: list[Chunk], vectors: list[list[float]], version: str
    ) -> None:
        if len(chunks) != len(vectors):
            raise ValueError("chunks and vectors length mismatch")
        try:
            with self._conn.cursor() as cur:
                if delete_paths:
                    cur.execute(
                        "DELETE FROM rag_chunks WHERE source_path = ANY(%s)",
                        (list(delete_paths),),
                    )
                cur.executemany(
                    """
                    INSERT INTO rag_chunks
                        (lang, level, chunk_type, lemma, title, content, source_path, source_hash,
                         patterns, prompts, embedding, create_version)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                    """,
                    [
                        (
                            c.lang,
                            c.level,
                            c.chunk_type,
                            c.lemma,
                            c.title,
                            c.content,
                            c.source_path,
                            c.source_hash,
                            c.patterns,
                            c.prompts,
                            vector,
                            version,
                        )
                        for c, vector in zip(chunks, vectors)
                    ],
                )
            self._conn.commit()
        except Exception:
            self._conn.rollback()
            raise

    def upsert_vocab_words(self, lang: str, rows: list[dict]) -> int:
        """按 (lang, lemma) upsert 到语种词表，保持 word_id 稳定；返回写入行数。"""
        if not rows:
            return 0
        columns = _VOCAB_COLUMNS_BY_LANG.get(lang)
        if columns is None:
            raise ValueError(f"unsupported vocab lang: {lang}")
        payload = [
            tuple(_vocab_column_value(row, column, lang) for column in columns)
            for row in rows
        ]
        placeholders = ", ".join(["%s"] * len(columns))
        updates = ", ".join(f"{column} = EXCLUDED.{column}" for column in columns)
        sql = f"""
            INSERT INTO vocab_words_{lang} ({", ".join(columns)})
            VALUES ({placeholders})
            ON CONFLICT (lemma) DO UPDATE SET
                {updates},
                updated_at = now()
        """
        try:
            with self._conn.cursor() as cur:
                for start in range(0, len(payload), 500):
                    cur.executemany(sql, payload[start : start + 500])
            self._conn.commit()
        except Exception:
            self._conn.rollback()
            raise
        return len(payload)

    def iter_chunks(self, lang: str) -> Iterator[StoredChunk]:
        with self._conn.cursor(name="snapshot_cursor") as cur:
            cur.itersize = 200
            cur.execute(
                """
                SELECT chunk_id, lang, level, chunk_type, lemma, title, content,
                       source_path, source_hash, embedding
                FROM rag_chunks WHERE lang = %s ORDER BY chunk_id
                """,
                (lang,),
            )
            for row in cur:
                yield StoredChunk(
                    chunk_id=row[0],
                    lang=row[1],
                    level=row[2],
                    chunk_type=row[3],
                    lemma=row[4],
                    title=row[5],
                    content=row[6],
                    source_path=row[7],
                    source_hash=row[8],
                    embedding=_vector_values(row[9]),
                )

    # 构建生命周期表（build_versions / build_logs）由 Go 独占写入；
    # builder 只把状态与结果打印成约定格式，由 scheduler 解析后入库。
    def record_build(self, version: str, lang: str, status: str, source_commit: str = "") -> None:
        print(
            f"[build] {version} status={status} lang={lang} source_commit={source_commit or '-'}",
            flush=True,
        )

    def finish_build(
        self,
        version: str,
        lang: str,
        db_key: str,
        vector_key: str,
        db_md5: str,
        vector_md5: str,
    ) -> None:
        payload = {
            "version": version,
            "lang": lang,
            "db_url": db_key,
            "vector_snapshot_url": vector_key,
            "db_md5": db_md5,
            "vector_md5": vector_md5,
        }
        print("BUILD_RESULT " + json.dumps(payload, ensure_ascii=False), flush=True)

    def fail_build(self, version: str, message: str) -> None:
        print(f"[build] {version} failed: {message}", flush=True)
        self.log(version, "pipeline", message, "error")

    def log(self, version: str, stage: str, message: str, level: str = "info") -> None:
        print(f"[{stage}] [{level}] {message}", flush=True)


# 语种词表可写列；与 Go 侧 store.vocabWriteColumns 保持一致。
_VOCAB_COLUMNS_BY_LANG: dict[str, tuple[str, ...]] = {
    "de": (
        "lemma", "level", "word_type", "gender", "plural",
        "chinese_mean", "english_mean", "example_de", "source_md_path",
    ),
    "en": (
        "lemma", "level", "word_type", "phonetic",
        "chinese_mean", "english_mean", "example_en", "source_md_path",
    ),
    "ja": (
        "lemma", "level", "word_type", "kana", "kanji", "romaji",
        "chinese_mean", "english_mean", "example_ja", "source_md_path",
    ),
}

_REQUIRED_VOCAB_COLUMNS = frozenset({"lemma", "level", "chinese_mean"})


def _vocab_column_value(row: dict, column: str, lang: str) -> str | None:
    """把一行 CSV 数据映射到语种表列；日语假名/汉字从 extra_json 提取。"""
    if column in _REQUIRED_VOCAB_COLUMNS:
        return row.get(column, "")
    if column in {"kana", "kanji"}:
        return _none(_extra_json(row).get(column))
    return _none(row.get(column, ""))


def _extra_json(row: dict) -> dict:
    raw = (row.get("extra_json") or "").strip()
    if not raw:
        return {}
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return data if isinstance(data, dict) else {}


def _vector_values(value) -> list[float]:
    """pgvector 返回 Vector 对象，统一转成 float 列表。"""
    if hasattr(value, "to_list"):
        value = value.to_list()
    return [float(v) for v in value]


def _none(value: str | None) -> str | None:
    """CSV 与词条 md 里的空串统一写成 NULL。"""
    if value is None:
        return None
    value = value.strip()
    return value or None
