"""语种插件注册表；新增语种只需在此登记插件。"""

from __future__ import annotations

from ..config import BuildError
from .base import LangPlugin
from .plugin_de import DePlugin
from .plugin_en import EnPlugin
from .plugin_ja import JaPlugin

_PLUGINS: dict[str, LangPlugin] = {
    plugin.lang: plugin for plugin in (DePlugin(), EnPlugin(), JaPlugin())
}

SUPPORTED_LANGS = tuple(_PLUGINS)


def get_plugin(lang: str) -> LangPlugin:
    try:
        return _PLUGINS[lang]
    except KeyError as exc:
        raise BuildError(f"unsupported lang {lang!r}; supported: {', '.join(SUPPORTED_LANGS)}") from exc
