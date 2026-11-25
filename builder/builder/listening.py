"""听力素材导入：下载 URL → 音/视频 → 德语音转写 → 按题号切分 → 视频抽帧 + OCR → 绑定官方答案。"""

from __future__ import annotations

import csv
import json
import logging
import os
import re
import resource
import shutil
import subprocess
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import numpy as np
from faster_whisper import WhisperModel

logger = logging.getLogger(__name__)

# 题目分界：Goethe/telc 听力里通常是 "Nummer 1"、"Frage 2"、"Aufgabe 3"。
# \b 前缀避免把 Rufnummer / Zimmernummer 这类合成词误当成题号提示。
CUE_RE = re.compile(r"\b(nummer|nr\.?|frage|aufgabe)\s+(\d{1,3}|[a-zäöüß]+)", re.IGNORECASE)
# Teil 2 的判断题常只出现裸题号（"Sieben?"、"10."），单独一行时视为题号提示。
BARE_NUM_RE = re.compile(r"^(\d{1,3}|[a-zäöüß]+)[\s\.\!\?,;:]*$", re.IGNORECASE)
# 相邻题号跨越过大的阈值，超过则视为 ASR 误识别（如把 "Rufnummer 089" 听成第 89 题）
MAX_CUE_JUMP = 10
# "Ende Teil 2" 中的 Teil 2 是结束提示，不能当作下一部分的开始
ENDE_TEIL_RE = re.compile(r"ende\s+teil\s+\w+", re.IGNORECASE)
NUMBER_WORDS = {
    "eins": 1, "zwei": 2, "drei": 3, "vier": 4, "fünf": 5, "fuenf": 5,
    "sechs": 6, "sieben": 7, "acht": 8, "neun": 9, "zehn": 10,
    "elf": 11, "zwölf": 12, "zwoelf": 12, "dreizehn": 13, "vierzehn": 14,
    "fünfzehn": 15, "sechzehn": 16, "siebzehn": 17, "achtzehn": 18,
    "neunzehn": 19, "zwanzig": 20,
}
SAFE_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")

# 题号播报（"Nummer 1"）与开场考试说明不属于题目内容，切片时跳过。
# 正文起点就是报号结束时刻；再往后加间隔会切进下一句的第一个词。
INSTRUCTION_LEAD_SECONDS = 0.2
# 开场说明的识别特征：Teil 页头、答题指令、"Sie hören ..." 等。
INSTRUCTION_RE = re.compile(
    r"(h(?:ö|o)ren\s+teil|teil\s+\d|teil\s+(?:eins|zwei|drei)"
    r"|h(?:ö|o)ren\s+sie|sie\s+h(?:ö|o)ren|was\s+(?:ist|1st)\s+richtig"
    r"|kreuzen\s+s|nummer|nr\.|frage|aufgabe)",
    re.IGNORECASE,
)
# 图片页头：整帧截图顶部会带 "Hören Teil 1 / Was ist richtig? / Kreuzen Sie an ..."。
HEADER_LINE_RE = re.compile(
    r"(h(?:ö|o)ren\s+teil|teil\s+\d|teil\s+(?:eins|zwei|drei)"
    r"|h(?:ö|o)ren\s+sie|sie\s+h(?:ö|o)ren|was\s+(?:ist|1st)\s+richtig"
    r"|kreuzen\s+s|goethe|zertifikat|deutsch\s+lernen|transkription)",
    re.IGNORECASE,
)

# 考试规则文件目录：按考试类型描述 Teil 划分、题号范围和读法。
PROFILES_DIR = Path(__file__).with_name("profiles")
PROFILE_ID_RE = re.compile(r"^[a-z0-9-]{1,40}$")


@dataclass(frozen=True)
class ProfileSection:
    name: str
    cues: tuple[str, ...]
    end_cues: tuple[str, ...]
    start_number: int
    end_number: int


@dataclass(frozen=True)
class ExamProfile:
    id: str
    name: str
    lang: str
    total_questions: int
    number_keywords: tuple[str, ...]
    sections: tuple[ProfileSection, ...]

    def cue_re(self) -> re.Pattern[str]:
        keywords = "|".join(re.escape(k) for k in self.number_keywords)
        return re.compile(rf"\b({keywords})\s+(\d{{1,3}}|[a-zäöüß]+)", re.IGNORECASE)


def load_exam_profile(profile_id: str | None) -> ExamProfile | None:
    """按 id 加载考试规则；为空时返回 None，沿用通用正则规则。"""
    if not profile_id:
        return None
    if not PROFILE_ID_RE.match(profile_id):
        raise ListeningError(f"非法的 profile id：{profile_id}")
    path = PROFILES_DIR / f"{profile_id}.json"
    if not path.is_file():
        raise ListeningError(f"未找到考试规则：{profile_id}")
    raw = json.loads(path.read_text(encoding="utf-8"))
    sections = tuple(
        ProfileSection(
            name=str(item["name"]),
            cues=tuple(str(c).lower() for c in item["cues"]),
            end_cues=tuple(str(c).lower() for c in item.get("end_cues", [])),
            start_number=int(item["from"]),
            end_number=int(item["to"]),
        )
        for item in raw["sections"]
    )
    return ExamProfile(
        id=str(raw.get("id", profile_id)),
        name=str(raw.get("name", profile_id)),
        lang=str(raw.get("lang", "de")),
        total_questions=int(raw.get("total_questions", 0)),
        number_keywords=tuple(str(k).lower() for k in raw.get("number_keywords", ["nummer"])),
        sections=sections,
    )

# OCR：tesseract 可用才启用，不可用时静默降级。
HAS_TESSERACT = bool(shutil.which("tesseract"))


def _stage(stage: str, message: str) -> None:
    """向 stdout 输出机器可读的阶段标记。

    Go 侧（scheduler）解析该行后更新任务通知并推 SSE；普通日志仍走 logging。
    """
    print(f"STAGE {json.dumps({'stage': stage, 'message': message}, ensure_ascii=False)}", flush=True)


class ListeningError(RuntimeError):
    """下载 / 转写 / 切分失败。"""


# #region debug-point A:stage-instrumentation
def _stub_stage() -> str:
    configured = os.getenv("LISTEN_STUB_AFTER", "").strip()
    if configured:
        return configured
    try:
        return (Path(os.getenv("WORK_DIR", "work")) / "listen_stub_after").read_text(encoding="utf-8").strip()
    except OSError:
        return ""


def _debug_probe(hypothesis: str, point: str) -> None:
    if os.getenv("LISTEN_DEBUG") != "1" and not _stub_stage():
        return
    usage = resource.getrusage(resource.RUSAGE_SELF)
    logger.info(
        "[DEBUG] sessionId=listening-prod-stall runId=%s hypothesisId=%s location=%s "
        "cpu_user=%.2f cpu_sys=%.2f maxrss_kb=%d",
        os.getenv("LISTEN_DEBUG_RUN_ID", "pre-fix"), hypothesis, point,
        usage.ru_utime, usage.ru_stime, usage.ru_maxrss,
    )


def _stub_after(stage: str) -> None:
    if _stub_stage() == stage:
        logger.warning("[DEBUG-STUB] stop after %s", stage)
        raise ListeningError(f"debug stub stop after {stage}")
# #endregion


@dataclass(frozen=True)
class Word:
    start: float
    end: float
    text: str


@dataclass
class Segment:
    start: float
    end: float
    text: str
    speaker: str | None = None  # 对话场景下的 A/B/C；独白为 None
    # 词级时间戳：ASR 常把上一题尾句和下一题的 "Nummer 4" 并进同一 segment，
    # 只有词级时间才能定位题号提示在 segment 内部的真实起点。
    words: tuple[Word, ...] = ()


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
    profile_id: str | None = None,
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

    # #region debug-point A:control
    _debug_probe("A", "before_download")
    _stub_after("start")
    # #endregion
    title = _probe_title(url, cookies)
    _stage("downloading", "开始下载音视频")
    logger.info("下载 url=%s", url)
    media = _download_best(url, download_dir, cookies)
    is_video = _has_video_stream(media)
    duration = _probe_duration(media)
    logger.info("已保存 %s（%.1fs video=%s）", media.name, duration, is_video)
    _stage("downloaded", f"音视频已保存：{media.name}")
    # #region debug-point F:after-download
    _debug_probe("F", "after_download")
    _stub_after("download")
    # #endregion

    # 音频轨（独立抽出来，避免后续转码重复解码视频）
    audio_source = media
    if is_video:
        # 音视频同步 + 抽音频合并为一次 ffmpeg 解码，避免整片被读两遍
        audio_source = download_dir / "audio_source.m4a"
        media = _prepare_media(media, audio_source)
    # #region debug-point C:after-media
    _debug_probe("C", "after_media_prepare")
    _stub_after("media")
    # #endregion

    # 直接解码为 16k 单声道 PCM 传给 Whisper，不落盘中间 wav（减少磁盘写入）
    _stage("parsing_pending", "媒体处理完成，等待开始转写")
    logger.info("解码音频（内存中，不落盘）")
    audio, sample_rate = _decode_audio(audio_source)
    # #region debug-point B:after-decode
    _debug_probe("B", "after_pcm_decode")
    _stub_after("decode")
    # #endregion
    logger.info("开始转写 model=%s language=%s", model_name, target_lang)
    _stage("parsing", f"开始转写 model={model_name}")
    segments = _transcribe(audio, work_root, model_name, target_lang)
    logger.info("转写完成 segments=%d", len(segments))
    # #region debug-point A:after-transcribe
    _debug_probe("A", "after_transcribe")
    _stub_after("transcribe")
    # #endregion

    logger.info("开始说话人分离（对话题用 A/B/C 区分）")
    speaker_count = assign_speakers(segments, audio, sample_rate)
    is_dialogue = speaker_count > 1
    logger.info("说话人分离完成 speakers=%d dialogue=%s", speaker_count, is_dialogue)
    # #region debug-point A:after-speakers
    _debug_probe("A", "after_speakers")
    _stub_after("speakers")
    # #endregion

    profile = load_exam_profile(profile_id)
    if profile is not None:
        logger.info("使用考试规则 profile=%s（%s）", profile.id, profile.name)
    anchor_segments: list[Segment] | None = None
    if profile is not None and _profile_needs_anchor_repair(segments, duration, profile):
        # VAD 偶发把整句题号提示（如 "Nummer 4"）过滤掉；关掉 VAD 再转写一遍，
        # 只用它填补主转写缺失的题号，题目文本仍取自主转写，避免重复内容。
        logger.warning("部分 Teil/题号提示缺失，使用无 VAD 转写补齐锚点")
        _stage("parsing", "补齐题号锚点（无 VAD 转写）")
        anchor_segments = _transcribe(
            audio, work_root, model_name, target_lang,
            vad_filter=False, progress=True, progress_label="补转写",
        )
    questions = split_questions(
        segments, duration, profile=profile, anchor_segments=anchor_segments
    )
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
            frame_path = image_dir / f"q{number:02d}.jpg"
            image_time = question.pop("image_time")
            try:
                _extract_frame(media, frame_path, image_time)
                question["image"] = f"images/q{number:02d}.jpg"
            except ListeningError as e:
                logger.debug("抽帧失败（跳过该题图）：%s", e)
                question["image"] = None
            question["image_time"] = image_time
            if ocr_enabled and HAS_TESSERACT and question["image"]:
                ocr_text = _ocr_frame(frame_path, lang=target_lang)
                # 最后一题时长会延伸到片尾，可能抽到"答案页"；命中时往前重抽一帧
                if _looks_like_answer_key(ocr_text):
                    retry_at = question["start"] + (image_time - question["start"]) * 0.4
                    try:
                        _extract_frame(media, frame_path, retry_at)
                        ocr_text = _ocr_frame(frame_path, lang=target_lang)
                        question["image_time"] = round(retry_at, 2)
                    except ListeningError:
                        pass
                question["ocr_text"] = ocr_text
                # 从 OCR 文本中提取选项（选择题 a/b/c/d）
                question["options"] = _parse_options(ocr_text)
            elif ocr_enabled and not HAS_TESSERACT:
                question["ocr_text"] = None
                question["ocr_note"] = "tesseract 未安装，跳过 OCR"
            else:
                question["ocr_text"] = None
            # 裁掉整帧顶部的考试说明页头，只保留本题题面。
            if question["image"] and HAS_TESSERACT:
                if _trim_image_header(frame_path, lang=target_lang):
                    logger.info("已裁掉第 %d 题图片页头", number)

        logger.info("第 %d/%d 题 完成 number=%d clip=%s", idx, len(questions), number, clip.name)

    # #region debug-point C:after-clips
    _debug_probe("C", "after_clips")
    _stub_after("clips")
    # #endregion
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
        "profile": profile.id if profile else None,
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

    # zip 不再在解析阶段预生成：体积大且大多数任务不需要下载整包，
    # 交给管理台在用户点「打包下载」时按需生成，避免解析任务额外写一份全量副本。

    return manifest_path


def _word_positions(segment: Segment) -> list[tuple[int, Word]]:
    """把词级时间戳对齐到 segment.text 的字符下标；对齐失败时返回空列表。"""
    positions: list[tuple[int, Word]] = []
    cursor = 0
    for word in segment.words:
        word_text = word.text.strip()
        if not word_text:
            continue
        index = segment.text.find(word_text, cursor)
        if index < 0:
            return []
        positions.append((index, word))
        cursor = index + len(word_text)
    return positions


def _time_at(segment: Segment, char_index: int) -> float:
    """估算 segment.text 中第 char_index 个字符对应的绝对时间。

    优先用词级时间戳精确定位；没有词级时间戳（旧转写结果）时按字符比例回退。
    """
    text = segment.text
    char_index = min(max(char_index, 0), len(text))
    if char_index == 0:
        return segment.start
    if char_index >= len(text):
        return segment.end
    for index, word in reversed(_word_positions(segment)):
        if char_index >= index:
            length = max(1, len(word.text.strip()))
            ratio = min(1.0, (char_index - index) / length)
            return word.start + ratio * (word.end - word.start)
    ratio = char_index / max(1, len(text))
    return segment.start + ratio * (segment.end - segment.start)


def _cue_bounds(segment: Segment, match: "re.Match[str]") -> tuple[float, float]:
    """题号提示的起止时间；无词级时间戳时按字符比例估算。

    报号整句只有题号与标点时（"Nummer 3."），直接取整句结束，
    避免比例估算把句末标点带进下一题。
    """
    start = _time_at(segment, match.start())
    tail = segment.text[match.end():].strip(" .!?,;:-–—")
    if not tail:
        return start, segment.end
    end = min(segment.end, max(start, _time_at(segment, match.end())))
    return start, end


def _text_index_at(segment: Segment, time: float) -> int:
    """_time_at 的逆运算：把绝对时间换算成 segment.text 的字符下标。"""
    text = segment.text
    if time <= segment.start:
        return 0
    if time >= segment.end:
        return len(text)
    for index, word in _word_positions(segment):
        if time < word.end:
            if time <= word.start:
                return index
            length = max(1, len(word.text.strip()))
            ratio = (time - word.start) / max(1e-6, word.end - word.start)
            return index + int(round(ratio * length))
    ratio = (time - segment.start) / max(1e-6, segment.end - segment.start)
    return int(round(ratio * len(text)))


def _slice_segment(segment: Segment, start: float, end: float) -> Segment | None:
    """把 segment 裁剪到 [start, end)，跨边界时按词级时间（或字符比例）截断文本。"""
    seg_start = max(segment.start, start)
    seg_end = min(segment.end, end)
    if seg_end <= seg_start:
        return None
    left = _text_index_at(segment, seg_start) if seg_start > segment.start else 0
    right = _text_index_at(segment, seg_end) if seg_end < segment.end else len(segment.text)
    text = segment.text[left:right].strip() if right > left else ""
    if not text:
        return None
    words = tuple(
        word
        for word in segment.words
        if seg_start <= (word.start + word.end) / 2 <= seg_end
    )
    return Segment(start=seg_start, end=seg_end, text=text, speaker=segment.speaker, words=words)


def _make_question(number: int, clip_start: float, clip_end: float, rows_all: list[Segment]) -> dict:
    """按时间区间生成题目记录，文本区间与音频区间保持一致。

    跨题边界的 segment 会被裁剪：上一题尾句留在上一题，下一题的题号提示
    归入下一题，避免 "Nummer 3 的结尾跑到 Nummer 4 的头部"。
    """
    clip_start = max(0.0, clip_start)
    clip_end = max(clip_start + 1.0, clip_end)
    image_time = clip_start + (clip_end - clip_start) * 0.45
    rows: list[Segment] = []
    for row in rows_all:
        if row.end <= clip_start or row.start >= clip_end:
            continue
        if row.start >= clip_start and row.end <= clip_end:
            rows.append(row)
            continue
        # 只与题目区间轻微重叠的 segment 视为噪声，避免把半句残片带进来
        if min(row.end, clip_end) - max(row.start, clip_start) < 0.2:
            continue
        sliced = _slice_segment(row, clip_start, clip_end)
        if sliced is not None:
            rows.append(sliced)
    question_speakers = sorted({row.speaker for row in rows if row.speaker})
    return {
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


def _norm_text(text: str) -> str:
    return re.sub(r"\s+", " ", text.lower()).strip()


def _fill_section_positions(
    section: ProfileSection,
    section_start: float,
    section_end: float,
    found: list[tuple[int, float]],
) -> dict[int, float]:
    """把该 Teil 内的题号补齐成连续序列。

    found 是按时间升序、已识别出的 (题号, 起点)。缺失的题号在相邻锚点间等分；
    段首/段尾缺失时分别向 Teil 开头、结尾等分，保证题号范围完整。
    """
    numbers = range(section.start_number, section.end_number + 1)
    if not found:
        step = (section_end - section_start) / max(1, len(numbers))
        return {n: section_start + step * i for i, n in enumerate(numbers)}

    positions: dict[int, float] = {}
    for index, (number, start) in enumerate(found):
        positions[number] = start
        if index + 1 < len(found):
            next_number, next_start = found[index + 1]
            gap = next_number - number
            if gap > 1:
                step = (next_start - start) / gap
                for k in range(1, gap):
                    positions[number + k] = start + step * k

    first_number, first_start = found[0]
    if first_number > section.start_number:
        missing = first_number - section.start_number
        step = (first_start - section_start) / missing
        for k in range(missing):
            positions[section.start_number + k] = section_start + step * k

    last_number, last_start = found[-1]
    if last_number < section.end_number:
        missing = section.end_number - last_number
        step = (section_end - last_start) / (missing + 1)
        for k in range(1, missing + 1):
            positions[last_number + k] = last_start + step * k

    return positions


def _find_section_starts(rows: Iterable[Segment], profile: ExamProfile) -> dict[str, float]:
    """定位每个 Teil 的起点；找不到的 Teil 不会出现在结果里。"""
    section_starts: dict[str, float] = {}
    for row in rows:
        # "Ende Teil 2" 里的 "teil 2" 不是 Teil 2 的开始，先剔除再匹配
        text = ENDE_TEIL_RE.sub(" ", _norm_text(row.text))
        for section in profile.sections:
            if section.name in section_starts:
                continue
            if any(cue in text for cue in section.cues):
                section_starts[section.name] = row.start
    return section_starts


def _section_anchor_rows(
    rows: Iterable[Segment], section: ProfileSection, section_start: float, section_limit: float
) -> list[Segment]:
    return [row for row in rows if section_start <= row.start < section_limit]


def _collect_section_anchors(
    rows: Iterable[Segment],
    section: ProfileSection,
    section_start: float,
    section_limit: float,
    cue_re: re.Pattern[str],
) -> list[tuple[int, float, float]]:
    """收集一个 Teil 内已识别到的题号。

    返回 (题号, 提示起点, 正文起点)：
    - 提示起点同时作为上一题的结束边界，保证上一题不包含本题报号；
    - 正文起点在报号结束之后，作为本题音频起点，去掉 "Nummer N"。
    题号必须递增且落在本 Teil 范围内。
    """
    found: list[tuple[int, float, float]] = []
    for row in _section_anchor_rows(rows, section, section_start, section_limit):
        matches = list(cue_re.finditer(row.text))
        for match in matches:
            number = _cue_number(match.group(2))
            if number is None:
                continue
            # 题号提示可能与上一题尾句同处一个 segment，取提示本身的词级起点
            cue_time, cue_end = _cue_bounds(row, match)
            # 只接受落在本 Teil 题号范围内的锚点，过滤电话号码等假数字
            if not (section.start_number <= number <= section.end_number):
                continue
            if found and number <= found[-1][0]:
                continue
            found.append((number, cue_time, cue_end))
        if matches:
            continue
        bare = BARE_NUM_RE.match(row.text.strip())
        if bare:
            number = _cue_number(bare.group(1))
            if number is None:
                continue
            if not (section.start_number <= number <= section.end_number):
                continue
            if found and number <= found[-1][0]:
                continue
            found.append((number, row.start, row.end))
    return found


def _profile_needs_anchor_repair(
    rows_all: list[Segment], duration: float, profile: ExamProfile
) -> bool:
    """判断是否有影响切分精度的锚点缺失。

    ASR 的 VAD 偶尔会把一整段（含 "Nummer 4"）过滤掉，导致题号只能靠等分补齐。
    只有当缺失题号跨越较长音频（等分会明显偏）时才值得再跑一遍无 VAD 转写。
    """
    section_starts = _find_section_starts(rows_all, profile)
    if len(section_starts) < len(profile.sections):
        return True
    cue_re = profile.cue_re()
    for index, section in enumerate(profile.sections):
        section_limit = (
            section_starts[profile.sections[index + 1].name]
            if index + 1 < len(profile.sections)
            else duration
        )
        found = _collect_section_anchors(
            rows_all, section, section_starts[section.name], section_limit, cue_re
        )
        if not found:
            # 整个 Teil 一个题号都没识别到，等分补齐全靠猜，值得补转写
            return True
        for (number, start, _), (next_number, next_start, _) in zip(found, found[1:]):
            if next_number - number > 1 and next_start - start > 45:
                return True
    return False


def _first_content_after_instructions(
    rows: Iterable[Segment], section_start: float, section_limit: float
) -> float:
    """跳过 Teil 页头与答题指令，返回第一段正文的起点。

    用于整段题号提示缺失、只能从 Teil 起点等分的首题，避免把开场的
    "Teil 1 / Was ist richtig? / Kreuzen Sie an ..." 当成题目音频。
    """
    for row in rows:
        if row.start < section_start or row.start >= section_limit:
            continue
        if INSTRUCTION_RE.search(row.text):
            continue
        return max(section_start, row.start - INSTRUCTION_LEAD_SECONDS)
    return section_start


def _split_with_profile(
    rows_all: list[Segment],
    duration: float,
    profile: ExamProfile,
    anchor_rows: list[Segment] | None = None,
) -> list[dict]:
    """按考试规则切分：先找 Teil，再在各 Teil 的题号范围内找题号。

    anchor_rows 只用于找题号锚点（可包含无 VAD 补转写的结果），
    题目文本仍取自 rows_all，避免重复内容进入 transcript。
    """
    anchors_all = anchor_rows if anchor_rows is not None else rows_all
    section_starts = _find_section_starts(rows_all, profile)
    if len(section_starts) < len(profile.sections):
        # 主转写缺少 Teil 锚点时，用补转写再找一遍
        section_starts = _find_section_starts([*rows_all, *anchors_all], profile)

    if len(section_starts) < len(profile.sections):
        logger.warning(
            "profile %s 只找到 %d/%d 个 Teil 锚点，回退通用切分",
            profile.id, len(section_starts), len(profile.sections),
        )
        return _split_with_legacy(rows_all, duration)

    cue_re = profile.cue_re()
    # starts_by_number 是每题的结束边界（下一题的报号起点），
    # content_starts_by_number 是本题音频起点（报号结束之后）。
    starts_by_number: dict[int, float] = {}
    content_starts_by_number: dict[int, float] = {}
    last_question_end: dict[int, float] = {}

    for index, section in enumerate(profile.sections):
        section_start = section_starts[section.name]
        section_limit = (
            section_starts[profile.sections[index + 1].name]
            if index + 1 < len(profile.sections)
            else duration
        )
        # "Ende Teil X" 之后是下一部分的说明，不应算进本题音频。
        # 结束提示也可能只出现在补转写里，所以两路结果一起找。
        content_rows = [*rows_all, *anchors_all] if anchor_rows is not None else rows_all
        content_end = section_limit
        for row in _section_anchor_rows(content_rows, section, section_start, section_limit):
            text = _norm_text(row.text)
            if any(cue in text for cue in section.end_cues):
                content_end = min(content_end, row.start)

        found = _collect_section_anchors(
            rows_all, section, section_start, section_limit, cue_re
        )
        if anchor_rows is not None:
            # 补转写可能时间略有漂移：主转写已找到的题号保持原锚点，
            # 只用补转写填补缺失题号。
            have = {number for number, _, _ in found}
            extra = _collect_section_anchors(
                anchors_all, section, section_start, section_limit, cue_re
            )
            found.extend(
                anchor for anchor in extra if anchor[0] not in have
            )
            found.sort(key=lambda item: item[0])
            deduped: list[tuple[int, float, float]] = []
            for anchor in found:
                if deduped and anchor[0] <= deduped[-1][0]:
                    continue
                deduped.append(anchor)
            found = deduped
        positions = _fill_section_positions(
            section, section_start, content_end, [(n, cue) for n, cue, _ in found]
        )
        starts_by_number.update(positions)
        content_starts_by_number.update(positions)
        # 有真实报号的题，正文起点取报号结束之后；缺失报号的题沿用等分位置。
        for number, _, content_start in found:
            content_starts_by_number[number] = content_start
        # 首题没有报号时，跳过开场说明再确定正文起点。
        if section.start_number not in {n for n, _, _ in found}:
            content_starts_by_number[section.start_number] = (
                _first_content_after_instructions(rows_all, section_start, content_end)
            )
        last_question_end[section.end_number] = content_end

    expected = [
        number
        for section in profile.sections
        for number in range(section.start_number, section.end_number + 1)
    ]
    if profile.total_questions and len(expected) != profile.total_questions:
        logger.warning(
            "profile %s 题号范围合计 %d，与 total_questions=%d 不一致",
            profile.id, len(expected), profile.total_questions,
        )

    questions: list[dict] = []
    for index, number in enumerate(expected):
        start = content_starts_by_number[number]
        end = starts_by_number[expected[index + 1]] if index + 1 < len(expected) else duration
        # 每个 Teil 的最后一题截止到 "Ende Teil X"，避免把下一部分的说明带进本题
        end = min(end, last_question_end.get(number, end))
        questions.append(_make_question(number, start, end, rows_all))
    return questions


def split_questions(
    segments: Iterable[Segment],
    duration: float,
    profile: ExamProfile | None = None,
    *,
    anchor_segments: Iterable[Segment] | None = None,
) -> list[dict]:
    """按题号提示切分，返回题目列表；没有可用提示时返回空列表。

    转写质量不稳时会出现两类问题，这里一并处理：
    1. 一句里挤进多个题号（"Nummer 4, Nummer 5, Nummer 6"）→ 全部收集
    2. 题号整句被漏识别（如 "Nummer 2" 没转出来）→ 相邻锚点之间等分补齐

    anchor_segments 可选：额外用于定位题号锚点（例如关闭 VAD 的补转写结果），
    题目文本仍由 segments 生成。
    """
    rows_all = list(segments)
    if profile is not None:
        anchors = list(anchor_segments) if anchor_segments is not None else None
        return _split_with_profile(rows_all, duration, profile, anchor_rows=anchors)
    return _split_with_legacy(rows_all, duration)


def _split_with_legacy(rows_all: list[Segment], duration: float) -> list[dict]:
    """通用规则：不依赖考试类型，只按 "Nummer/Nr." 锚点切分。"""
    anchors: list[tuple[int, float, float]] = []

    def accept(number: int | None, cue_start: float, content_start: float) -> None:
        if number is None:
            return
        if anchors and number <= anchors[-1][0]:
            return
        if anchors and number > anchors[-1][0] + MAX_CUE_JUMP:
            return
        anchors.append((number, cue_start, content_start))

    for row in rows_all:
        matches = list(CUE_RE.finditer(row.text))
        if matches:
            for match in matches:
                # 与 profile 模式一致：用题号提示自身的词级起止时间
                cue_start, cue_end = _cue_bounds(row, match)
                accept(_cue_number(match.group(2)), cue_start, cue_end)
            continue
        # 整句只有一个裸题号（Teil 2 常见）
        bare = BARE_NUM_RE.match(row.text.strip())
        if bare:
            accept(_cue_number(bare.group(1)), row.start, row.end)

    if not anchors:
        return []

    # 相邻锚点之间按题号数量等分，补齐漏识别的题号
    cues: list[tuple[int, float, float]] = []
    for index, (number, cue_start, content_start) in enumerate(anchors):
        if index + 1 < len(anchors):
            next_number, next_cue, _ = anchors[index + 1]
            count = next_number - number
            cue_step = (next_cue - cue_start) / count
            for k in range(count):
                cue = cue_start + cue_step * k
                # 缺失题号的题目无法判断报号长度，沿用等分位置作为正文起点。
                cues.append((number + k, cue, content_start if k == 0 else cue))
        else:
            cues.append((number, cue_start, content_start))

    questions: list[dict] = []
    for index, (number, cue_start, content_start) in enumerate(cues):
        # 本题音频从报号之后开始，到下一题报号起点结束，报号本身不进入任何一题。
        end = cues[index + 1][1] if index + 1 < len(cues) else duration
        questions.append(_make_question(number, content_start, end, rows_all))
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


def _normalize_answer(raw: str) -> str:
    """归一化答案写法：选择 A/B/C/D；判断 R（正确）/F（错误）。"""
    s = raw.strip().lower()
    if s == "a":
        return "A"
    if s == "b":
        return "B"
    if s == "c":
        return "C"
    if s == "d":
        return "D"
    if s in ("r", "richtig", "true", "t"):
        return "R"
    if s in ("f", "falsch", "false"):
        return "F"
    raise ListeningError(f"不支持的答案 {raw!r}（支持 A/B/C/D 或 richtig/falsch）")


def load_answers(path: Path) -> dict[int, str]:
    """读取官方答案 CSV：题号,答案；允许带表头。

    答案支持：选择 A/B/C/D；判断 richtig/falsch（或 r/f、true/false）。
    归一化后存储：A/B/C/D（选择）、R（正确）/F（错误）。
    """
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
            result[number] = _normalize_answer(cells[1])
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
        tail = proc.stderr or proc.stdout or ""
        tail_lines = tail.strip().splitlines()[-5:]
        msg = f"命令失败（{cmd[0]}）：{' | '.join(tail_lines)}"
        # B 站对机房 IP 有反爬风控，会直接返回 412，此时需要登录态 cookies
        if "412" in tail and any("bilibili" in arg.lower() for arg in cmd):
            msg += "。B 站返回 412（反爬风控），当前服务器 IP 被拦截，请在上传时附带已登录 B 站的 cookies.txt"
        raise ListeningError(msg)
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
    """下载媒体（优先 720p 以内的视频轨保留画面，无视频则只下音频）。

    听力素材只需要音频转写 + 偶尔抽帧 OCR，1080p/4K 只会成倍增加下载与磁盘写入，
    因此限制高度不超过 720；源站没有 720p 时才回退到最佳可用清晰度。
    """
    template = out_dir / "source.%(ext)s"
    cmd = [
        "yt-dlp",
        "--no-warnings",
        "--no-playlist",
        "-f",
        "bestvideo[height<=720]+bestaudio/best[height<=720]/bestvideo*+bestaudio/best",
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
    # 导致画面倍速而音频正常；同步与抽音频在 import_from_url 里合并为一次解码。
    return media


def _prepare_media(media: Path, audio_target: Path) -> Path:
    """一次 ffmpeg 完成音视频同步 + 抽取音频轨，返回同步后的媒体路径。

    之前 _sync_av（整片读+写）和 _extract_audio（再整片读一遍）分别执行，
    同一视频被完整读取两次；合并成单次 demux 后只读一遍，降低解析期磁盘读取。
    """
    tmp = media.with_suffix(".synced" + media.suffix)
    try:
        _run([
            "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
            "-i", str(media),
            "-map", "0", "-c", "copy",
            "-fflags", "+genpts", "-movflags", "+faststart", str(tmp),
            "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "48000",
            "-c:a", "aac", "-b:a", "96k", str(audio_target),
        ])
        tmp.replace(media)
        return media
    except ListeningError as e:
        logger.warning("音视频同步失败，使用原始文件：%s", e)
        if tmp.exists():
            tmp.unlink()
        _extract_audio(media, audio_target)
        return media


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
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error", "-threads", "1", "-i", str(source),
         "-vn", "-ac", "1", "-ar", "48000", "-c:a", "aac", "-b:a", "96k", str(target)]
    )


def _decode_audio(source: Path) -> tuple[np.ndarray, int]:
    """用 ffmpeg 把任意媒体解码为 16k 单声道 float32 PCM，全程不落盘。

    之前会先写一份 source16k.wav 再读回内存，长音频动辄几十到上百 MB；
    这里直接走 stdout 管道，磁盘上不再产生中间 wav。
    """
    cmd = [
        "ffmpeg", "-v", "error", "-threads", "1", "-i", str(source),
        "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1",
    ]
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert proc.stdout is not None and proc.stderr is not None
    chunks: list[bytes] = []
    while True:
        chunk = proc.stdout.read(4 * 1024 * 1024)
        if not chunk:
            break
        chunks.append(chunk)
    stderr = proc.stderr.read().decode("utf-8", "replace")
    return_code = proc.wait()
    proc.stdout.close()
    proc.stderr.close()
    if return_code != 0:
        tail = " | ".join(stderr.strip().splitlines()[-5:])
        raise ListeningError(f"音频解码失败：{tail}")
    data = b"".join(chunks)
    if not data:
        raise ListeningError("音频解码结果为空，请检查媒体文件")
    audio = np.frombuffer(data, dtype=np.int16).astype(np.float32) / 32768.0
    return audio, 16000


def _transcribe(
    audio: np.ndarray,
    work_root: Path,
    model_name: str,
    language: str,
    *,
    vad_filter: bool = True,
    progress: bool = True,
    progress_label: str = "转写中",
) -> list[Segment]:
    cache = Path(work_root) / ".models"
    cache.mkdir(parents=True, exist_ok=True)
    logger.info("加载 Whisper 模型 model=%s device=cpu compute=int8 cache=%s", model_name, cache)
    # 2C 小机器上 Whisper 默认会吃满所有核心，导致同容器里的 Go API 和 PG 被饿死。
    # 默认单线程，把另一核留给常驻服务；CPU 充裕的机器可用 WHISPER_CPU_THREADS 调大。
    cpu_threads = max(1, int(os.getenv("WHISPER_CPU_THREADS", "1")))
    model = WhisperModel(
        model_name,
        device="cpu",
        compute_type="int8",
        download_root=str(cache),
        cpu_threads=cpu_threads,
        num_workers=1,
    )
    logger.info("Whisper 模型加载完成，开始转写（可能耗时较久）")
    segments_iter, _info = model.transcribe(
        audio,
        language=language,
        vad_filter=vad_filter,
        beam_size=5,
        condition_on_previous_text=False,
        # 词级时间戳用于定位 segment 内部的题号提示（如 "ich Handball. Nummer 4."）
        word_timestamps=True,
    )
    # faster_whisper 的 segments 是 generator，边解包边打进度
    segments: list[Segment] = []
    total_seconds = float(getattr(_info, "duration", 0.0) or 0.0)
    for seg in segments_iter:
        segments.append(Segment(
            start=round(seg.start, 2),
            end=round(seg.end, 2),
            text=seg.text.strip(),
            words=tuple(
                Word(start=round(word.start, 2), end=round(word.end, 2), text=word.word)
                for word in (seg.words or [])
            ),
        ))
        if len(segments) % 20 == 0:
            logger.info("转写中已处理 %d 个 segment（最后时刻 %.1fs）",
                        len(segments), seg.end)
            if progress:
                # STAGE 会同步到管理台/desktop 的进度显示，避免长转写看起来卡死
                _stage("parsing", f"{progress_label}：{seg.end:.0f}s / {total_seconds:.0f}s")
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


def assign_speakers(
    segments: list[Segment],
    audio: np.ndarray,
    sample_rate: int,
    max_speakers: int = 3,
) -> int:
    """轻量说话人分离：基频 + 频谱质心聚类，对话场景给 segment 打 A/B/C 标签。

    不依赖 pyannote（CPU 上更快、无需模型下载）。返回识别出的说话人数；
    返回 1 表示独白，此时所有 segment.speaker 保持 None。
    """
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
        # 与题目清单一致：跨题边界的 segment 按时间裁剪后再展示
        rows: list[Segment] = []
        for seg in segments:
            if seg.end <= start or seg.start >= end:
                continue
            if seg.start >= start and seg.end <= end:
                rows.append(seg)
                continue
            sliced = _slice_segment(seg, start, end)
            if sliced is not None:
                rows.append(sliced)
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


# 每题音频首尾各补 1 秒静音：开头留出准备时间，结尾避免和下一题紧贴。
CLIP_GAP_SECONDS = 1.0


def _cut_clip(source: Path, target: Path, start: float, end: float) -> None:
    """截取 [start, end) 并首尾补 CLIP_GAP_SECONDS 静音。

    - `-ss/-t` 作为输入选项：ffmpeg 会先 seek 再精确丢弃到时间点，
      既快又不会像 `-to` 那样把上一题的尾音带进来；
    - `adelay` 在开头插入静音，`apad` 在结尾补齐静音；
    - 重新编码 AAC，保证拼接后的时长精确等于「内容时长 + 两段 gap」。
    """
    duration = max(0.5, end - start)
    gap_ms = int(CLIP_GAP_SECONDS * 1000)
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
         "-ss", f"{start:.2f}", "-t", f"{duration:.2f}",
         "-threads", "1", "-i", str(source),
         "-vn",
         "-af", f"adelay={gap_ms}:all=1,apad=pad_dur={CLIP_GAP_SECONDS}",
         "-c:a", "aac", "-b:a", "96k", str(target)]
    )


def _extract_frame(source: Path, target: Path, at_time: float) -> None:
    # 抽帧只用于题目配图 + OCR，JPEG 比 PNG 小一个数量级，写入压力更低
    _run(
        ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
         "-ss", f"{at_time:.2f}", "-threads", "1", "-i", str(source),
         "-frames:v", "1", "-q:v", "3", str(target)]
    )


def _probe_image_size(path: Path) -> tuple[int, int]:
    """读取图片宽高；失败返回 (0, 0)。"""
    try:
        proc = _run(
            ["ffprobe", "-v", "error", "-select_streams", "v:0",
             "-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", str(path)],
            allow_stderr=True,
        )
        width, height = proc.stdout.strip().splitlines()[0].split("x")[:2]
        return int(width), int(height)
    except (ListeningError, ValueError, IndexError):
        return 0, 0


def _ocr_lines(image: Path, lang: str = "de") -> list[tuple[str, int, int]]:
    """用 tesseract TSV 输出识别到的文本行：[(文本, top, bottom)]，按 top 升序。"""
    if not HAS_TESSERACT:
        return []
    tess_lang = TESSERACT_LANG_MAP.get(lang, lang)
    try:
        proc = subprocess.run(
            ["tesseract", str(image), "-", "-l", tess_lang, "--psm", "6", "tsv"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
        )
    except OSError:
        return []
    if proc.returncode != 0:
        return []

    groups: dict[tuple[str, str, str], dict] = {}
    for row in csv.DictReader(proc.stdout.splitlines(), delimiter="\t"):
        text = (row.get("text") or "").strip()
        if not text:
            continue
        key = (row.get("block_num") or "", row.get("par_num") or "", row.get("line_num") or "")
        group = groups.setdefault(key, {"text": [], "top": 10**9, "bottom": 0})
        group["text"].append(text)
        try:
            top = int(row.get("top") or 0)
            height = int(row.get("height") or 0)
        except ValueError:
            continue
        group["top"] = min(group["top"], top)
        group["bottom"] = max(group["bottom"], top + height)

    lines = [(" ".join(g["text"]), g["top"], g["bottom"]) for g in groups.values()]
    lines.sort(key=lambda item: item[1])
    return lines


def _trim_image_header(image: Path, lang: str = "de") -> bool:
    """裁掉整帧截图顶部的考试说明（"Hören Teil 1 / Was ist richtig? ..."）。

    题目图片是视频整帧，页头说明与本题无关。按 OCR 行位置找到页头之后
    第一行正文，裁掉其上方的页头，保留宽度与其余内容；无法可靠定位时不裁剪。
    """
    width, height = _probe_image_size(image)
    if width <= 0 or height <= 0:
        return False

    header_bottom = 0
    content_top = 0
    for text, top, bottom in _ocr_lines(image, lang):
        if HEADER_LINE_RE.search(text):
            header_bottom = max(header_bottom, bottom)
            continue
        content_top = top
        break

    if header_bottom <= 0 or content_top <= 0:
        return False
    cut = content_top - 8
    cut = max(cut, header_bottom)
    cut -= cut % 2
    # 只裁顶部少量区域，避免误识别把整道题裁掉
    if cut < 4 or cut > height * 0.5 or height - cut < height * 0.4:
        return False

    temp = image.with_name(f"{image.stem}.crop{image.suffix}")
    try:
        _run(
            ["ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
             "-i", str(image), "-vf", f"crop={width}:{height - cut}:0:{cut}",
             "-frames:v", "1", "-q:v", "2", str(temp)]
        )
    except ListeningError:
        temp.unlink(missing_ok=True)
        return False
    os.replace(temp, image)
    return True


TESSERACT_LANG_MAP = {
    "de": "deu", "en": "eng", "ja": "jpn",
    "deu": "deu", "eng": "eng", "jpn": "jpn",
}

# 答案页特征（片尾常见 "Lösungen" 或「题号-答案」对照表），抽帧命中时要重抽
ANSWER_KEY_RE = re.compile(r"lösungen", re.IGNORECASE)
ANSWER_PAIR_RE = re.compile(r"\b\d{1,2}\s*[-–,.\s]?\s*[rRfFa-dA-D](?![a-zA-Zäöüß])")


def _looks_like_answer_key(ocr_text: str | None) -> bool:
    """判断 OCR 结果是否为答案页（片尾对照表），是则说明这帧抽错了位置。"""
    if not ocr_text:
        return False
    if ANSWER_KEY_RE.search(ocr_text):
        return True
    # 至少两组「题号+答案」，避免把正文里的孤立匹配当成答案页
    return len(ANSWER_PAIR_RE.findall(ocr_text)) >= 2

# 选项标记：真实考卷复选框被 OCR 成 "[a |"、"a]"、"[b|"、"(2 |"（2 是 a 的误识别）等形态。
# 匹配「可选左括号 + 字母 + 右括号/竖线」，另允许行首裸字母（"a zum Arzt ..."）。
OPTION_MARK_RE = re.compile(
    r"(?:[\[\(I|1]\s*)?([a-cA-C2])\s*[\]\|\)]\s*"
    r"|^([a-cA-C])\s+"
)
# Teil 3 的选项常各占一行，行首即为标记（"12| von Freitag..."、"Ib| von Freitagabend..."、"al Morgen"）
OPTION_LINE_RE = re.compile(
    r"^(?:[\[\(I|1]?\s*([a-cA-C2])\s*[\]\|\)lL1]|([a-cA-C])\s+)"
)
# OCR 字符 → 选项下标（a/b/c；"2" 是被误识别的 a）
OPTION_INDEX = {"a": 0, "2": 0, "b": 1, "c": 2, "d": 3}
# 中文/德语卷面噪声行，命中则丢弃
OPTION_NOISE_RE = re.compile(r"(was ist richtig|kreuzen sie|sie hören|deutsch lernen|transkriptionen)", re.IGNORECASE)


def _clean_option(text: str) -> str:
    """去掉选项文本两端的括号、竖线、空白与残留噪声。"""
    cleaned = re.sub(r"[\[\]\|\)\(]+", " ", text).strip(" .-—_")
    cleaned = re.sub(r"\s{2,}", " ", cleaned).strip()
    return cleaned


def _parse_options(ocr_text: str | None) -> list[str]:
    """从 OCR 文本里提取选择题选项（a/b/c），返回按字母顺序排列的选项文本。"""
    if not ocr_text:
        return []

    best: dict[int, str] = {}
    for line in ocr_text.splitlines():
        if OPTION_NOISE_RE.search(line):
            continue
        marks = list(OPTION_MARK_RE.finditer(line))
        # 一行里至少两个标记才认为这行是选项行，避免把正文里的字母误当选项
        if len(marks) < 2:
            continue
        found: dict[int, str] = {}
        for index, mark in enumerate(marks):
            letter = (mark.group(1) or mark.group(2) or "").lower()
            slot = OPTION_INDEX.get(letter)
            if slot is None or slot in found:
                continue
            end = marks[index + 1].start() if index + 1 < len(marks) else len(line)
            text = _clean_option(line[mark.end():end])
            if len(text) < 2 or not any(c.isalnum() for c in text):
                continue
            found[slot] = text
        if len(found) > len(best):
            best = found

    # 兜底：选项各占一行时逐行收集
    if len(best) < 2:
        per_line: dict[int, str] = {}
        for line in ocr_text.splitlines():
            if OPTION_NOISE_RE.search(line):
                continue
            match = OPTION_LINE_RE.match(line.strip())
            if not match:
                continue
            letter = (match.group(1) or match.group(2) or "").lower()
            slot = OPTION_INDEX.get(letter)
            if slot is None or slot in per_line:
                continue
            text = _clean_option(line.strip()[match.end():])
            if len(text) < 2 or not any(c.isalnum() for c in text):
                continue
            per_line[slot] = text
        if len(per_line) >= 2:
            best = per_line

    return [best[i] for i in sorted(best)]


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
