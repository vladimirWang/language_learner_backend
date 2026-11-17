"""听力素材导入：下载 URL → 音/视频 → 德语音转写 → 按题号切分 → 视频抽帧 + OCR → 绑定官方答案。"""

from __future__ import annotations

import csv
import json
import logging
import re
import shutil
import subprocess
import wave
import zipfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import numpy as np
from faster_whisper import WhisperModel

logger = logging.getLogger(__name__)

# 题目分界：Goethe/telc 听力里通常是 "Nummer 1"、"Frage 2"、"Aufgabe 3"。
CUE_RE = re.compile(r"^\s*(nummer|frage|aufgabe)\s+(\d{1,3})\b", re.IGNORECASE)
NUMBER_WORDS = {
    "eins": 1, "zwei": 2, "drei": 3, "vier": 4, "fünf": 5, "fuenf": 5,
    "sechs": 6, "sieben": 7, "acht": 8, "neun": 9, "zehn": 10,
    "elf": 11, "zwölf": 12, "zwoelf": 12, "dreizehn": 13, "vierzehn": 14,
    "fünfzehn": 15, "sechzehn": 16, "siebzehn": 17, "achtzehn": 18,
    "neunzehn": 19, "zwanzig": 20,
}
SAFE_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")

# OCR：tesseract 可用才启用，不可用时静默降级。
HAS_TESSERACT = bool(shutil.which("tesseract"))


class ListeningError(RuntimeError):
    """下载 / 转写 / 切分失败。"""


@dataclass
class Segment:
    start: float
    end: float
    text: str
    speaker: str | None = None  # 对话场景下的 A/B/C；独白为 None


def import_from_url(
    url: str,
    work_root: Path,
    exam_id: str,
    *,
    lang: str = "de",
    answers: Path | None = None,
    cookies: Path | None = None,
    model_name: str = "small",
    target_lang: str = "de",
    ocr_enabled: bool = True,
) -> Path:
    """完整导入流程，返回写出的 manifest 路径。"""
    if not SAFE_ID_RE.match(exam_id):
        raise ListeningError("exam_id 只能包含字母、数字、下划线和中划线")
    if not url.startswith(("http://", "https://")):
        raise ListeningError("url 必须以 http:// 或 https:// 开头")

    exam_dir = work_root / "listening" / exam_id
    download_dir = exam_dir / "source"
    audio_dir = exam_dir / "audio"
    image_dir = exam_dir / "images"
    if exam_dir.exists():
        shutil.rmtree(exam_dir)
    download_dir.mkdir(parents=True)
    audio_dir.mkdir(parents=True)
    image_dir.mkdir(parents=True)

    title = _probe_title(url, cookies)
    logger.info("下载 url=%s", url)
    media = _download_best(url, download_dir, cookies)
    is_video = _has_video_stream(media)
    duration = _probe_duration(media)
    logger.info("已保存 %s（%.1fs video=%s）", media.name, duration, is_video)

    # 音频轨（独立抽出来，避免后续转码重复解码视频）
    audio_source = media
    if is_video:
        audio_source = download_dir / "audio_source.m4a"
        _extract_audio(media, audio_source)

    wav = download_dir / "source16k.wav"
    _to_wav(audio_source, wav)
    logger.info("开始转写 model=%s language=%s", model_name, target_lang)
    segments = _transcribe(wav, work_root, model_name, target_lang)
    logger.info("转写完成 segments=%d", len(segments))

    logger.info("开始说话人分离（对话题用 A/B/C 区分）")
    speaker_count = assign_speakers(segments, wav)
    is_dialogue = speaker_count > 1
    logger.info("说话人分离完成 speakers=%d dialogue=%s", speaker_count, is_dialogue)

    questions = split_questions(segments, duration)
    if not questions:
        raise ListeningError("没有识别到题目分界（Nummer/Frage/Aufgabe），请检查音频或人工校对转写")

    answer_map = load_answers(answers) if answers else {}
    logger.info("识别到 %d 道题，开始切片 + 抽帧 + OCR", len(questions))
    for idx, question in enumerate(questions, start=1):
        number = question["number"]
        if number in answer_map:
            question["answer"] = answer_map[number]

        # 音频切片
        clip = audio_dir / f"q{number:02d}.m4a"
        _cut_clip(audio_source, clip, question["start"], question["end"])

        # 视频抽帧 + OCR（有视频轨时）
        if is_video:
            frame_path = image_dir / f"q{number:02d}.png"
            try:
                _extract_frame(media, frame_path, question["image_time"])
                question["image"] = f"images/q{number:02d}.png"
            except ListeningError as e:
                logger.debug("抽帧失败（跳过该题图）：%s", e)
                question["image"] = None
            question["image_time"] = question.pop("image_time")
            if ocr_enabled and HAS_TESSERACT and question["image"]:
                question["ocr_text"] = _ocr_frame(frame_path, lang=target_lang)
            elif ocr_enabled and not HAS_TESSERACT:
                question["ocr_text"] = None
                question["ocr_note"] = "tesseract 未安装，跳过 OCR"
            else:
                question["ocr_text"] = None

        logger.info("第 %d/%d 题 完成 number=%d clip=%s", idx, len(questions), number, clip.name)

    manifest = {
        "exam_id": exam_id,
        "lang": lang,
        "title": title or exam_id,
        "source_url": url,
        "media_has_video": is_video,
        "media_file": f"source/{media.name}",
        "model": model_name,
        "duration": round(duration, 2),
        "ocr_enabled": ocr_enabled,
        "speaker_count": speaker_count,
        "is_dialogue": is_dialogue,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "question_count": len(questions),
        "questions": questions,
    }
    manifest_path = exam_dir / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")

    transcript_files = write_transcript_files(exam_dir, manifest, questions, segments)
    logger.info(
        "转写文件已写出：%s", ", ".join(path.name for path in transcript_files)
    )
    logger.info("导入完成 manifest=%s questions=%d", manifest_path, len(questions))

    # 清理转写中间产物（16k wav 体积大，转写完成后无保留价值）
    wav_path = download_dir / "source16k.wav"
    if wav_path.exists():
        wav_path.unlink()
        logger.info("已清理中间产物 %s", wav_path.name)

    # 预生成 zip 包（Store 不压缩），供管理后台直接下载，避免请求时临时打包。
    zip_path = exam_dir / f"{exam_id}.zip"
    try:
        with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_STORED) as zf:
            for path in exam_dir.rglob("*"):
                if not path.is_file():
                    continue
                if path.name == zip_path.name:
                    continue
                if path.name == "source16k.wav":
                    continue
                zf.write(path, path.relative_to(exam_dir).as_posix())
        logger.info("zip 包已生成：%s (%.1f MB)", zip_path.name, zip_path.stat().st_size / 1024 / 1024)
    except OSError as e:
        logger.warning("生成 zip 失败（不影响主流程）：%s", e)

    return manifest_path


def split_questions(segments: Iterable[Segment], duration: float) -> list[dict]:
    """按题号提示切分，返回题目列表；没有可用提示时返回空列表。"""
    rows_all = list(segments)
    cues: list[tuple[int, float]] = []
    seen: set[int] = set()
    for row in rows_all:
        match = CUE_RE.match(row.text)
        if not match:
            continue
        number = _cue_number(match.group(2))
        if number is None or number in seen:
            continue
        if cues and number <= cues[-1][0]:
            continue
        seen.add(number)
        cues.append((number, row.start))

    questions: list[dict] = []
    for index, (number, start) in enumerate(cues):
        end = cues[index + 1][1] if index + 1 < len(cues) else duration
        clip_start = max(0.0, start - 0.3)
        clip_end = max(clip_start + 1.0, end - 0.3)
        # 视频抽帧选每题中段（题干刚出、选项画面稳定）
        image_time = clip_start + (clip_end - clip_start) * 0.45
        rows = [row for row in rows_all if start - 0.2 <= (row.start + row.end) / 2 < end - 0.2]
        question_speakers = sorted({row.speaker for row in rows if row.speaker})
        questions.append(
            {
                "number": number,
                "start": round(clip_start, 2),
                "end": round(clip_end, 2),
                "image_time": round(image_time, 2),
                "audio": f"audio/q{number:02d}.m4a",
                "image": None,
                "ocr_text": None,
                "transcript": _join_with_speakers(rows),
                "speakers": question_speakers,
                "answer": None,
                "options": [],
            }
        )
    return questions


def _join_with_speakers(rows: list[Segment]) -> str:
    """把题目内的 segment 拼成文本；一位以上说话人时按 A:/B: 分行标注。"""
    speakers = {row.speaker for row in rows if row.speaker}
    if len(speakers) < 2:
        return " ".join(row.text for row in rows).strip()

    lines: list[str] = []
    current: str | None = None
    buffer: list[str] = []
    for row in rows:
        who = row.speaker or "?"
        if who != current:
            if buffer and current:
                lines.append(f"{current}: {' '.join(buffer)}")
            current = who
            buffer = [row.text]
        else:
            buffer.append(row.text)
    if buffer and current:
        lines.append(f"{current}: {' '.join(buffer)}")
    return "\n".join(lines)


def load_answers(path: Path) -> dict[int, str]:
    """读取官方答案 CSV：题号,答案；允许带表头。"""
    if not path.exists():
        raise ListeningError(f"答案文件不存在：{path}")
    result: dict[int, str] = {}
    with path.open("r", encoding="utf-8-sig", newline="") as handle:
        for raw in csv.reader(handle):
            cells = [cell.strip() for cell in raw if cell.strip()]
            if len(cells) < 2:
                continue
            try:
                number = int(cells[0].lstrip("qQ"))
            except ValueError:
                continue
            answer = cells[1].upper()
            if answer not in {"A", "B", "C", "D"}:
                raise ListeningError(f"第 {number} 题答案不是 A/B/C/D：{cells[1]}")
            result[number] = answer
    if not result:
        raise ListeningError("答案文件里没有解析到任何题号")
    return result


def _cue_number(raw: str) -> int | None:
    if raw.isdigit():
        return int(raw)
    return NUMBER_WORDS.get(raw.lower())


def _run(cmd: list[str], *, cwd: Path | None = None, allow_stderr: bool = False) -> subprocess.CompletedProcess[str]:
    logger.debug("run: %s", " ".join(cmd))
    proc = subprocess.run(
        cmd, cwd=cwd, capture_output=True, text=True, encoding="utf-8", errors="replace"
    )
    if proc.returncode != 0:
        tail = (proc.stderr or proc.stdout or "").strip().splitlines()[-5:]
        raise ListeningError(f"命令失败（{cmd[0]}）：{' | '.join(tail)}")
    if not allow_stderr and proc.stderr.strip():
        logger.debug("%s stderr: %s", cmd[0], proc.stderr.strip()[:300])
    return proc


def _probe_title(url: str, cookies: Path | None) -> str:
    cmd = ["yt-dlp", "--no-warnings", "--skip-download", "--print", "%(title)s"]
    if cookies:
        cmd += ["--cookies", str(cookies)]
    cmd.append(url)
    try:
        return _run(cmd, allow_stderr=True).stdout.strip().splitlines()[0]
    except ListeningError:
        return ""


def _download_best(url: str, out_dir: Path, cookies: Path | None) -> Path:
    """下载最佳可用媒体（优先视频轨保留画面，无视频则只下音频）。"""
    template = out_dir / "source.%(ext)s"
    cmd = [
        "yt-dlp",
        "--no-warnings",
        "--no-playlist",
        "-f",
        "bestvideo*+bestaudio/best",
        "--merge-output-format", "mp4",
        "-o",
        str(template),
    ]
    if cookies:
        cmd += ["--cookies", str(cookies)]
    cmd.append(url)
    _run(cmd, allow_stderr=True)

    candidates = [
        path for path in out_dir.iterdir()
        if path.name.startswith("source.") and path.suffix.lower() not in {".part", ".ytdl"}
    ]
    if not candidates:
        raise ListeningError("下载完成后没有找到媒体文件")
    media = max(candidates, key=lambda path: path.stat().st_size)

    # 分离下载的视频流和音频流可能时间戳不匹配（B站常见），
    # 导致画面倍速而音频正常。重新生成 PTS 并强制同步。
    if _has_video_stream(media):
        _sync_av(media)
    return media


def _sync_av(media: Path) -> None:
    """重新生成时间戳并强制音视频同步，覆盖原文件。"""
    tmp = media.with_suffix(".synced" + media.suffix)
    try:
        _run([
            "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
            "-i", str(media),
            "-c", "copy",
            "-fflags", "+genpts",
            "-async", "1",
            "-movflags", "+faststart",
            str(tmp),
        ])
        tmp.replace(media)
    except ListeningError as e:
        logger.warning("音视频同步失败，使用原始文件：%s", e)
        if tmp.exists():
            tmp.unlink()


def _probe_duration(path: Path) -> float:
    proc = _run(
        [
            "ffprobe", "-v", "error", "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1", str(path),
        ]
    )
    try:
        return float(proc.stdout.strip())
    except ValueError as exc:
        raise ListeningError(f"无法读取时长：{path.name}") from exc


def _has_video_stream(path: Path) -> bool:
    proc = _run(
        [
            "ffprobe", "-v", "error", "-select_streams", "v",
            "-show_entries", "stream=codec_type",
            "-of", "default=noprint_wrappers=1:nokey=1", str(path),
        ]
    )
    return "video" in proc.stdout.lower()


def _extract_audio(source: Path, target: Path) -> None:
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error", "-i", str(source),
         "-vn", "-ac", "1", "-ar", "48000", "-c:a", "aac", "-b:a", "96k", str(target)]
    )


def _to_wav(source: Path, target: Path) -> None:
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error", "-i", str(source),
         "-vn", "-ac", "1", "-ar", "16000", str(target)]
    )


def _read_wav(path: Path) -> tuple[np.ndarray, int]:
    with wave.open(str(path), "rb") as handle:
        if handle.getsampwidth() != 2:
            raise ListeningError("仅支持 16bit PCM 音频")
        sample_rate = handle.getframerate()
        frames = handle.readframes(handle.getnframes())
    audio = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
    return audio, sample_rate


def _transcribe(wav: Path, work_root: Path, model_name: str, language: str) -> list[Segment]:
    cache = Path(work_root) / ".models"
    cache.mkdir(parents=True, exist_ok=True)
    logger.info("加载 Whisper 模型 model=%s device=cpu compute=int8 cache=%s", model_name, cache)
    model = WhisperModel(model_name, device="cpu", compute_type="int8", download_root=str(cache))
    logger.info("Whisper 模型加载完成，开始转写（可能耗时较久）")
    audio, _sample_rate = _read_wav(wav)
    segments_iter, _info = model.transcribe(
        audio,
        language=language,
        vad_filter=True,
        beam_size=5,
        condition_on_previous_text=False,
    )
    # faster_whisper 的 segments 是 generator，边解包边打进度
    segments: list[Segment] = []
    for seg in segments_iter:
        segments.append(Segment(
            start=round(seg.start, 2),
            end=round(seg.end, 2),
            text=seg.text.strip(),
        ))
        if len(segments) % 20 == 0:
            logger.info("转写中已处理 %d 个 segment（最后时刻 %.1fs）",
                        len(segments), seg.end)
    logger.info("转写完成 segments=%d", len(segments))
    return [seg for seg in segments if seg.text.strip()]


def _pitch_hz(chunk: np.ndarray, sample_rate: int, fmin: float = 60.0, fmax: float = 350.0) -> float:
    """自相关估计基频（Hz）；静音或不可靠时返回 0。"""
    chunk = chunk - chunk.mean()
    if chunk.size < sample_rate // 20 or not np.any(np.abs(chunk) > 1e-4):
        return 0.0
    corr = np.correlate(chunk, chunk, mode="full")[chunk.size - 1:]
    min_lag = max(1, int(sample_rate / fmax))
    max_lag = min(corr.size - 1, int(sample_rate / fmin))
    if max_lag <= min_lag:
        return 0.0
    peak = int(np.argmax(corr[min_lag:max_lag])) + min_lag
    if corr[peak] < 0.25 * corr[0]:
        return 0.0
    return float(sample_rate / peak)


def _segment_feature(audio: np.ndarray, sample_rate: int, seg: Segment) -> np.ndarray | None:
    """取 segment 中间 1 秒，返回 [基频, 频谱质心(kHz)]；无有效语音时返回 None。"""
    start = int(seg.start * sample_rate)
    end = int(seg.end * sample_rate)
    if end - start < sample_rate // 10:
        return None
    chunk = audio[start:end]
    mid = chunk.size // 2
    half = min(chunk.size // 2, sample_rate // 2)
    chunk = chunk[mid - half: mid + half]
    if chunk.size < sample_rate // 20:
        return None

    pitch = _pitch_hz(chunk, sample_rate)
    if pitch <= 0:
        return None

    spectrum = np.abs(np.fft.rfft(chunk * np.hanning(chunk.size)))
    freqs = np.fft.rfftfreq(chunk.size, 1.0 / sample_rate)
    total = float(spectrum.sum())
    centroid = float((spectrum * freqs).sum() / total / 1000.0) if total > 0 else 0.0
    return np.array([pitch, centroid], dtype=np.float64)


def _kmeans(points: np.ndarray, k: int, iterations: int = 25) -> np.ndarray:
    """极简 k-means（k-means++ 初始化），返回每个点的簇号。"""
    n = points.shape[0]
    if k <= 1 or n <= k:
        return np.zeros(n, dtype=int)

    rng = np.random.default_rng(0)
    centers = [points[int(rng.integers(n))]]
    for _ in range(1, k):
        dist2 = np.min(
            ((points[:, None, :] - np.array(centers)[None, :, :]) ** 2).sum(axis=2), axis=1
        )
        total = float(dist2.sum())
        if total <= 0:
            centers.append(points[int(rng.integers(n))])
        else:
            centers.append(points[int(rng.choice(n, p=dist2 / total))])
    matrix = np.array(centers, dtype=np.float64)

    labels = np.zeros(n, dtype=int)
    for _ in range(iterations):
        dist2 = ((points[:, None, :] - matrix[None, :, :]) ** 2).sum(axis=2)
        labels = dist2.argmin(axis=1)
        for j in range(k):
            members = points[labels == j]
            if members.shape[0]:
                matrix[j] = members.mean(axis=0)
    return labels


def _silhouette(points: np.ndarray, labels: np.ndarray) -> float:
    """平均轮廓系数，用于选择聚类数；越小越不可信。"""
    uniq = np.unique(labels)
    if uniq.size < 2:
        return -1.0
    scores: list[float] = []
    for i in range(points.shape[0]):
        own = points[labels == labels[i]]
        if own.shape[0] <= 1:
            continue
        a = float(np.linalg.norm(own - points[i], axis=1).sum() / (own.shape[0] - 1))
        b = min(
            float(np.linalg.norm(points[labels == other] - points[i], axis=1).mean())
            for other in uniq
            if other != labels[i]
        )
        denom = max(a, b)
        if denom > 0:
            scores.append((b - a) / denom)
    return float(np.mean(scores)) if scores else -1.0


def assign_speakers(segments: list[Segment], wav: Path, max_speakers: int = 3) -> int:
    """轻量说话人分离：基频 + 频谱质心聚类，对话场景给 segment 打 A/B/C 标签。

    不依赖 pyannote（CPU 上更快、无需模型下载）。返回识别出的说话人数；
    返回 1 表示独白，此时所有 segment.speaker 保持 None。
    """
    audio, sample_rate = _read_wav(wav)
    features: list[np.ndarray] = []
    holders: list[Segment] = []
    for seg in segments:
        feature = _segment_feature(audio, sample_rate, seg)
        if feature is not None:
            features.append(feature)
            holders.append(seg)
    if len(features) < 4:
        return 1

    matrix = np.array(features)
    std = matrix.std(axis=0)
    std[std < 1e-6] = 1.0
    normalized = (matrix - matrix.mean(axis=0)) / std

    best_labels: np.ndarray | None = None
    best_score = -1.0
    for k in range(2, max_speakers + 1):
        labels = _kmeans(normalized, k)
        counts = np.bincount(labels, minlength=k)
        if counts.min() < max(2, int(len(labels) * 0.1)):
            continue
        score = _silhouette(normalized, labels)
        if score > best_score:
            best_score = score
            best_labels = labels

    if best_labels is None or best_score < 0.25:
        logger.info("说话人分离置信度不足（score=%.2f），按独白处理", best_score)
        return 1

    order: list[int] = []
    for label in best_labels:
        if int(label) not in order:
            order.append(int(label))
    letter_of = {label: chr(ord("A") + index) for index, label in enumerate(order)}
    for seg, label in zip(holders, best_labels):
        seg.speaker = letter_of[int(label)]
    return len(order)


def _fmt_time(seconds: float) -> str:
    hours = int(seconds // 3600)
    minutes = int((seconds % 3600) // 60)
    return f"{hours:02d}:{minutes:02d}:{seconds % 60:05.2f}"


def _fmt_srt_time(seconds: float) -> str:
    total_ms = int(round(seconds * 1000))
    hours, total_ms = divmod(total_ms, 3_600_000)
    minutes, total_ms = divmod(total_ms, 60_000)
    secs, millis = divmod(total_ms, 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def write_transcript_files(
    exam_dir: Path,
    manifest: dict,
    questions: list[dict],
    segments: list[Segment],
) -> list[Path]:
    """把转写内容写成独立文件：transcript.json / transcript.txt / transcript.srt。"""
    span_by_number = {q["number"]: (q["start"], q["end"]) for q in questions}

    # 1) 结构化 JSON：整段 segment + 每题聚合
    payload = {
        "exam_id": manifest["exam_id"],
        "title": manifest["title"],
        "lang": manifest["lang"],
        "source_url": manifest["source_url"],
        "media_file": manifest.get("media_file"),
        "duration": manifest["duration"],
        "speaker_count": manifest.get("speaker_count", 1),
        "is_dialogue": manifest.get("is_dialogue", False),
        "speakers": sorted({seg.speaker for seg in segments if seg.speaker}),
        "segments": [
            {
                "start": seg.start,
                "end": seg.end,
                "speaker": seg.speaker,
                "text": seg.text,
            }
            for seg in segments
        ],
        "questions": [
            {
                "number": q["number"],
                "start": q["start"],
                "end": q["end"],
                "answer": q.get("answer"),
                "speakers": q.get("speakers", []),
                "transcript": q["transcript"],
            }
            for q in questions
        ],
    }
    json_path = exam_dir / "transcript.json"
    json_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")

    # 2) 人类可读文本：按题分段，对话按说话人分行
    lines: list[str] = [
        manifest["title"],
        f"来源: {manifest['source_url']}",
        f"时长: {_fmt_time(manifest['duration'])}",
        "说话人: "
        + (
            "、".join(payload["speakers"])
            if payload["is_dialogue"]
            else "独白（未检测到多人对话）"
        ),
        "",
    ]
    for question in questions:
        start, end = span_by_number[question["number"]]
        lines.append("=" * 76)
        lines.append(f"Frage {question['number']}  [{_fmt_time(start)} - {_fmt_time(end)}]")
        lines.append("=" * 76)
        rows = [
            seg
            for seg in segments
            if start - 0.2 <= (seg.start + seg.end) / 2 < end - 0.2
        ]
        for seg in rows:
            prefix = f"[{seg.speaker}] " if seg.speaker else ""
            lines.append(f"{prefix}{_fmt_time(seg.start)} - {_fmt_time(seg.end)}  {seg.text}")
        if not rows:
            lines.append("（本题没有识别到转写文本）")
        if question.get("answer"):
            lines.append(f"答案: {question['answer']}")
        lines.append("")
    text_path = exam_dir / "transcript.txt"
    text_path.write_text("\n".join(lines), encoding="utf-8")

    # 3) SRT 字幕：带说话人前缀，方便播放器挂载
    srt: list[str] = []
    for index, seg in enumerate(segments, start=1):
        prefix = f"[{seg.speaker}] " if seg.speaker else ""
        srt.append(str(index))
        srt.append(f"{_fmt_srt_time(seg.start)} --> {_fmt_srt_time(seg.end)}")
        srt.append(f"{prefix}{seg.text}")
        srt.append("")
    srt_path = exam_dir / "transcript.srt"
    srt_path.write_text("\n".join(srt), encoding="utf-8")

    return [json_path, text_path, srt_path]


def _cut_clip(source: Path, target: Path, start: float, end: float) -> None:
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
         "-ss", f"{start:.2f}", "-to", f"{end:.2f}",
         "-i", str(source), "-vn", "-c:a", "aac", "-b:a", "96k", str(target)]
    )


def _extract_frame(source: Path, target: Path, at_time: float) -> None:
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
         "-ss", f"{at_time:.2f}", "-i", str(source),
         "-frames:v", "1", "-q:v", "2", str(target)]
    )


TESSERACT_LANG_MAP = {
    "de": "deu", "en": "eng", "ja": "jpn",
    "deu": "deu", "eng": "eng", "jpn": "jpn",
}


def _ocr_frame(image: Path, lang: str = "de") -> str | None:
    """用 tesseract OCR 一张帧，返回清洗后的文字；未安装或失败返回 None。"""
    if not HAS_TESSERACT:
        return None
    tess_lang = TESSERACT_LANG_MAP.get(lang, lang)
    try:
        proc = subprocess.run(
            ["tesseract", str(image), "-", "-l", tess_lang, "--psm", "6"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
        if proc.returncode != 0:
            logger.debug("tesseract failed: %s", proc.stderr.strip()[:200])
            return None
    except OSError:
        return None

    lines = [line.strip() for line in proc.stdout.splitlines() if line.strip()]
    text = "\n".join(lines)
    # 过滤掉纯噪声
    if sum(len(line) for line in lines) < 3:
        return None
    return text
