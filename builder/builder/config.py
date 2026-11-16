"""构建任务配置：全部来自环境变量 / .env，不在代码中写死密钥。"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import quote

from dotenv import find_dotenv, load_dotenv


class BuildError(RuntimeError):
    """素材 / 配置 / 外部依赖错误，触发构建失败。"""


@dataclass(frozen=True)
class Settings:
    content_dir: Path
    work_dir: Path
    pg_dsn: str

    embedding_api_url: str
    embedding_api_key: str
    embedding_model: str
    embedding_dim: int
    embedding_batch: int

    s3_endpoint: str
    s3_bucket: str
    s3_access_key: str
    s3_secret_key: str
    s3_secure: bool

    @classmethod
    def from_env(cls) -> "Settings":
        load_dotenv(find_dotenv(usecwd=True))

        content_dir = _resolve_content_dir()
        work_dir = Path(os.getenv("WORK_DIR", "") or "work").resolve()

        endpoint = os.getenv("S3_ENDPOINT", "").strip()
        return cls(
            content_dir=content_dir,
            work_dir=work_dir,
            pg_dsn=_pg_dsn(),
            embedding_api_url=os.getenv("EMBEDDING_API_URL", "").strip(),
            embedding_api_key=os.getenv("EMBEDDING_API_KEY", "").strip(),
            embedding_model=os.getenv("EMBEDDING_MODEL", "bge-m3").strip(),
            embedding_dim=int(os.getenv("EMBEDDING_DIM", "1024")),
            embedding_batch=int(os.getenv("EMBEDDING_BATCH", "32")),
            s3_endpoint=endpoint,
            s3_bucket=os.getenv("S3_BUCKET", "").strip(),
            s3_access_key=os.getenv("S3_ACCESS_KEY", "").strip(),
            s3_secret_key=os.getenv("S3_SECRET_KEY", "").strip(),
            s3_secure=endpoint.lower().startswith("https://"),
        )


def _resolve_content_dir() -> Path:
    explicit = os.getenv("CONTENT_DIR", "").strip()
    if explicit:
        return Path(explicit).resolve()
    for candidate in (Path("content"), Path("../content")):
        if candidate.is_dir():
            return candidate.resolve()
    raise BuildError("content dir not found; set CONTENT_DIR")


def _pg_dsn() -> str:
    dsn = os.getenv("PG_DSN", "").strip()
    if dsn:
        return dsn
    user = quote(os.getenv("PG_USER", "postgres"), safe="")
    password = quote(os.getenv("PG_PASSWORD", ""), safe="")
    host = os.getenv("PG_HOST", "127.0.0.1")
    port = os.getenv("PG_PORT", "5432")
    database = quote(os.getenv("PG_DATABASE", "postgres"), safe="")
    sslmode = os.getenv("PG_SSLMODE", "disable")
    return f"postgresql://{user}:{password}@{host}:{port}/{database}?sslmode={sslmode}"
