"""外部 Embedding API 客户端；不在本机加载任何模型。"""

from __future__ import annotations

import logging
import time

import httpx

from .config import BuildError, Settings

logger = logging.getLogger(__name__)


class Embedder:
    def __init__(self, settings: Settings) -> None:
        if not settings.embedding_api_url:
            raise BuildError("EMBEDDING_API_URL is required")
        headers = {"Content-Type": "application/json"}
        if settings.embedding_api_key:
            headers["Authorization"] = f"Bearer {settings.embedding_api_key}"
        self._settings = settings
        self._client = httpx.Client(headers=headers, timeout=60.0)

    def close(self) -> None:
        self._client.close()

    def embed(self, texts: list[str]) -> list[list[float]]:
        vectors: list[list[float]] = []
        batch_size = max(1, self._settings.embedding_batch)
        for start in range(0, len(texts), batch_size):
            batch = texts[start : start + batch_size]
            vectors.extend(self._embed_batch(batch))
            logger.info("embedded %d/%d chunks", min(start + batch_size, len(texts)), len(texts))
        return vectors

    def _embed_batch(self, batch: list[str]) -> list[list[float]]:
        payload = {"model": self._settings.embedding_model, "input": batch}
        last_error: Exception | None = None
        for attempt in range(3):
            try:
                resp = self._client.post(self._settings.embedding_api_url, json=payload)
                resp.raise_for_status()
                return self._parse(resp.json(), len(batch))
            except (httpx.HTTPError, ValueError, BuildError) as exc:
                last_error = exc
                logger.warning("embedding attempt %d failed: %s", attempt + 1, exc)
                time.sleep(1 + attempt)
        raise BuildError(f"embedding api failed: {last_error}")

    def _parse(self, body: object, expected: int) -> list[list[float]]:
        data = body.get("data") if isinstance(body, dict) else body
        if not isinstance(data, list) or len(data) != expected:
            raise BuildError("unexpected embedding response shape")

        ordered = sorted(data, key=lambda item: item.get("index", 0) if isinstance(item, dict) else 0)
        vectors: list[list[float]] = []
        for item in ordered:
            vector = item.get("embedding") if isinstance(item, dict) else item
            if not isinstance(vector, list) or not vector:
                raise BuildError("embedding vector missing")
            if len(vector) != self._settings.embedding_dim:
                raise BuildError(
                    f"embedding dim mismatch: got {len(vector)}, expected {self._settings.embedding_dim}"
                )
            vectors.append([float(v) for v in vector])
        return vectors
