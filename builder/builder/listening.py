"""听力素材导入：下载 URL → 音/视频 → 德语音转写 → 按题号切分 → 视频抽帧 + OCR → 绑定官方答案。"""

from __future__ import annotations

import csv
import json
import logging
import re
import shutil
import subprocess
import wave
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
            _extract_frame(media, frame_path, question["image_time"])
            question["image"] = f"images/q{number:02d}.png"
            question["image_time"] = question.pop("image_time")
            if ocr_enabled and HAS_TESSERACT:
                question["ocr_text"] = _ocr_frame(frame_path, lang=target_lang)
            elif ocr_enabled and not HAS_TESSERACT:
                question["ocr_text"] = None
                question["ocr_note"] = "tesseract 未安装，跳过 OCR"

        logger.info("第 %d/%d 题 完成 number=%d clip=%s", idx, len(questions), number, clip.name)

    manifest = {
        "exam_id": exam_id,
        "lang": lang,
        "title": title or exam_id,
        "source_url": url,
        "media_has_video": is_video,
        "model": model_name,
        "duration": round(duration, 2),
        "ocr_enabled": ocr_enabled,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "question_count": len(questions),
        "questions": questions,
    }
    manifest_path = exam_dir / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    transcript_path = exam_dir / "transcript.json"
    transcript_path.write_text(
        json.dumps([seg.__dict__ for seg in segments], ensure_ascii=False, indent=1),
        encoding="utf-8",
    )
    logger.info("导入完成 manifest=%s questions=%d", manifest_path, len(questions))
    return manifest_path


def split_questions(segments: Iterable[Segment], duration: float) -> list[dict]:
    """按题号提示切分，返回题目列表；没有可用提示时返回空列表。"""
    rows = list(segments)
    cues: list[tuple[int, float]] = []
    seen: set[int] = set()
    for row in rows:
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
        text = " ".join(
            row.text for row in rows if start - 0.2 <= (row.start + row.end) / 2 < end - 0.2
        ).strip()
        questions.append(
            {
                "number": number,
                "start": round(clip_start, 2),
                "end": round(clip_end, 2),
                "image_time": round(image_time, 2),
                "audio": f"audio/q{number:02d}.m4a",
                "image": None,
                "ocr_text": None,
                "transcript": text,
                "answer": None,
                "options": [],
            }
        )
    return questions


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
    return max(candidates, key=lambda path: path.stat().st_size)


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


def _read_wav(path: Path) -> np.ndarray:
    with wave.open(str(path), "rb") as handle:
        if handle.getsampwidth() != 2:
            raise ListeningError("仅支持 16bit PCM 音频")
        frames = handle.readframes(handle.getnframes())
    return np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0


def _transcribe(wav: Path, work_root: Path, model_name: str, language: str) -> list[Segment]:
    cache = Path(work_root) / ".models"
    cache.mkdir(parents=True, exist_ok=True)
    logger.info("加载 Whisper 模型 model=%s device=cpu compute=int8 cache=%s", model_name, cache)
    model = WhisperModel(model_name, device="cpu", compute_type="int8", download_root=str(cache))
    logger.info("Whisper 模型加载完成，开始转写（可能耗时较久）")
    audio = _read_wav(wav)
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


def _ocr_frame(image: Path, lang: str = "de") -> str | None:
    """用 tesseract OCR 一张帧，返回清洗后的文字；未安装或失败返回 None。"""
    if not HAS_TESSERACT:
        return None
    try:
        proc = subprocess.run(
            ["tesseract", str(image), "-", "-l", lang, "--psm", "6"],
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
