"""构建期 PostgreSQL / pgvector 访问：chunk 入库、增量比对、版本与日志。"""

from __future__ import annotations

import logging
from collections.abc import Iterator
from dataclasses import dataclass

import psycopg
from pgvector.psycopg import register_vector

from .chunking import Chunk

logger = logging.getLogger(__name__)

_SCHEMA = [
    "CREATE EXTENSION IF NOT EXISTS vector",
    """
    CREATE TABLE IF NOT EXISTS rag_chunks (
        chunk_id    BIGSERIAL PRIMARY KEY,
        lang        TEXT NOT NULL,
        level       TEXT,
        chunk_type  TEXT NOT NULL,
        lemma       TEXT,
        title       TEXT NOT NULL,
        content     TEXT NOT NULL,
        source_path TEXT NOT NULL,
        source_hash TEXT NOT NULL,
        embedding   vector(1024),
        create_version TEXT,
        updated_at  TIMESTAMPTZ DEFAULT now()
    )
    """,
    """
    CREATE INDEX IF NOT EXISTS idx_rag_chunks_filter
        ON rag_chunks (lang, chunk_type, level)
    """,
    """
    CREATE TABLE IF NOT EXISTS build_versions (
        version   TEXT PRIMARY KEY,
        lang      TEXT,
        db_url    TEXT,
        vector_snapshot_url TEXT,
        db_md5    TEXT,
        vector_md5 TEXT,
        status    TEXT,
        source_commit TEXT,
        created_at TIMESTAMPTZ DEFAULT now()
    )
    """,
    """
    CREATE TABLE IF NOT EXISTS build_logs (
        log_id    BIGSERIAL PRIMARY KEY,
        version   TEXT,
        stage     TEXT,
        message   TEXT,
        level     TEXT,
        created_at TIMESTAMPTZ DEFAULT now()
    )
    """,
]


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
        # pgvector 适配器要求库里已存在 vector 类型
        with self._conn.cursor() as cur:
            cur.execute("CREATE EXTENSION IF NOT EXISTS vector")
        self._conn.commit()
        register_vector(self._conn)

    def close(self) -> None:
        self._conn.close()

    def ensure_schema(self) -> None:
        with self._conn.cursor() as cur:
            for statement in _SCHEMA:
                cur.execute(statement)
            # 兼容旧库：build_versions 增加 source_commit 列（增量迁移）
            cur.execute(
                """
                DO $$
                BEGIN
                    IF NOT EXISTS (
                        SELECT 1 FROM information_schema.columns
                        WHERE table_name = 'build_versions' AND column_name = 'source_commit'
                    ) THEN
                        ALTER TABLE build_versions ADD COLUMN source_commit TEXT;
                    END IF;
                END $$;
                """
            )
        self._conn.commit()

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
                        (lang, level, chunk_type, lemma, title, content, source_path, source_hash, embedding, create_version)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
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

    def record_build(self, version: str, lang: str, status: str, source_commit: str = "") -> None:
        with self._conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO build_versions (version, lang, status, source_commit)
                VALUES (%s, %s, %s, %s)
                ON CONFLICT (version) DO UPDATE
                SET lang = EXCLUDED.lang, status = EXCLUDED.status,
                    source_commit = COALESCE(NULLIF(%s, ''), build_versions.source_commit)
                """,
                (version, lang, status, source_commit, source_commit),
            )
        self._conn.commit()

    def finish_build(
        self,
        version: str,
        lang: str,
        db_key: str,
        vector_key: str,
        db_md5: str,
        vector_md5: str,
    ) -> None:
        with self._conn.cursor() as cur:
            cur.execute(
                """
                UPDATE build_versions
                SET status = 'built', db_url = %s, vector_snapshot_url = %s,
                    db_md5 = %s, vector_md5 = %s
                WHERE version = %s
                """,
                (db_key, vector_key, db_md5, vector_md5, version),
            )
            if cur.rowcount == 0:
                cur.execute(
                    """
                    INSERT INTO build_versions
                        (version, lang, status, db_url, vector_snapshot_url, db_md5, vector_md5)
                    VALUES (%s, %s, 'built', %s, %s, %s, %s)
                    """,
                    (version, lang, db_key, vector_key, db_md5, vector_md5),
                )
        self._conn.commit()

    def fail_build(self, version: str, message: str) -> None:
        try:
            with self._conn.cursor() as cur:
                cur.execute(
                    "UPDATE build_versions SET status = 'failed' WHERE version = %s",
                    (version,),
                )
            self._conn.commit()
        except Exception:  # pragma: no cover - 失败路径尽力而为
            logger.exception("failed to mark build %s as failed", version)
        self.log(version, "pipeline", message, "error")

    def log(self, version: str, stage: str, message: str, level: str = "info") -> None:
        try:
            with self._conn.cursor() as cur:
                cur.execute(
                    "INSERT INTO build_logs (version, stage, message, level) VALUES (%s, %s, %s, %s)",
                    (version, stage, message, level),
                )
            self._conn.commit()
        except Exception:  # pragma: no cover - 日志失败不阻断构建
            logger.exception("failed to write build log")


def _vector_values(value) -> list[float]:
    """pgvector 返回 Vector 对象，统一转成 float 列表。"""
    if hasattr(value, "to_list"):
        value = value.to_list()
    return [float(v) for v in value]
