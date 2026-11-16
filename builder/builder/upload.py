"""上传构建产物到 S3 兼容对象存储；未配置时保留本地路径（便于离线开发）。"""

from __future__ import annotations

import hashlib
import logging
from pathlib import Path

from minio import Minio

from .config import BuildError, Settings

logger = logging.getLogger(__name__)


class Uploader:
    def __init__(self, settings: Settings) -> None:
        self._bucket = settings.s3_bucket
        self._client: Minio | None = None
        if settings.s3_endpoint and settings.s3_bucket:
            endpoint = settings.s3_endpoint.removeprefix("https://").removeprefix("http://")
            self._client = Minio(
                endpoint,
                access_key=settings.s3_access_key,
                secret_key=settings.s3_secret_key,
                secure=settings.s3_secure,
            )
        else:
            logger.warning("S3 not configured, artifacts stay local")

    def upload(self, local_path: Path, key: str) -> str:
        if self._client is None:
            return str(local_path)
        try:
            if not self._client.bucket_exists(self._bucket):
                self._client.make_bucket(self._bucket)
            self._client.fput_object(self._bucket, key, str(local_path))
        except Exception as exc:  # minio 抛出的异常类型较杂，统一转构建错误
            raise BuildError(f"upload {key} failed: {exc}") from exc
        logger.info("uploaded %s -> s3://%s/%s", local_path.name, self._bucket, key)
        return key


def md5_file(path: Path) -> str:
    digest = hashlib.md5()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()
