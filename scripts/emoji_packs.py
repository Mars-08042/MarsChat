"""从开源表情仓库提取各平台的小表情，统一转成 WebP，并生成 manifest.json 和 README.md。

表情图片的版权属于各平台，不提交进仓库（设计 3.6.4）：源仓库按 SOURCES 中固定的提交克隆到 --sources，
输出目录默认是被 git 忽略的 web/public/emoji-packs。输出目录自带说明，可以整体复制到其他项目复用。

源仓库是不受信任的数据：只读取图片和 JSON，不执行其中任何脚本，跳过符号链接，用 python -I 运行。
依赖 Pillow。

用法：python -I scripts/emoji_packs.py --sources tmp/emoji-sources --out web/public/emoji-packs [--size 128]
"""

from __future__ import annotations

import argparse
import base64
import io
import json
import re
import shutil
import sys
from dataclasses import dataclass
from pathlib import Path

from PIL import Image, ImageSequence, ImageStat

GENERATOR = "marschat-emoji-packs"
MANIFEST_VERSION = 1

# 源仓库与固定提交。升级时先在预览里核对子集内容，再改这里的提交号
SOURCES = {
    "walinejs": {
        "dir": "walinejs-emojis",
        "repo": "https://github.com/walinejs/emojis",
        "commit": "b10c0abfdb7c52365d8f5a46b730ab9130de173f",
        "license": "GPL-3.0；README 声明表情版权归原作者（平台）所有",
    },
    "stickers": {
        "dir": "augenstern-stickers",
        "repo": "https://github.com/Augenstern-O/Stickers",
        "commit": "91285989c50eaf45560dab02043df1d4b03aded1",
        "license": "CC BY 4.0（仓库整体）；表情图片版权归各平台所有",
    },
    "bilicon": {
        "dir": "gweesin-bilicon",
        "repo": "https://github.com/gweesin/bilicon",
        "commit": "70b2b187a867a62b91fddf5505bd8ab52cb43e7f",
        "license": "MIT（代码与数据整理）；表情图片版权归各平台所有",
    },
}

# 平台顺序就是表情面板侧栏里的顺序。每个平台可以有多个子集，code = prefix + 名称，必须全局唯一。
# kind：waline 读 info.json；owo 按 Stickers 仓库 OwO.json 的顺序读目录；iconify 解码 JSON 里的 data URI
PLATFORMS = [
    {"id": "wechat", "name": "微信", "icon": "wechat_微笑", "sets": [
        {"id": "wechat", "name": "微信", "prefix": "wechat_", "source": "walinejs", "kind": "waline", "path": "wechat"},
    ]},
    {"id": "douyin", "name": "抖音", "icon": "douyin_666", "sets": [
        {"id": "douyin", "name": "抖音", "prefix": "douyin_", "source": "bilicon", "kind": "iconify", "path": "json/douyin.json"},
    ]},
    {"id": "bilibili", "name": "B站", "icon": "bili_tv_doge", "sets": [
        {"id": "bili", "name": "小黄脸", "prefix": "bili_", "source": "stickers", "kind": "owo", "path": "哔哩哔哩-小黄脸"},
        # 名称本身带 tv_ 前缀，code 形如 bili_tv_doge
        {"id": "bili-tv", "name": "小电视", "prefix": "bili_", "source": "stickers", "kind": "owo", "path": "哔哩哔哩-TV小电视"},
    ]},
    {"id": "tieba", "name": "贴吧", "icon": "tieba_antic", "sets": [
        {"id": "tieba", "name": "贴吧", "prefix": "tieba_", "source": "walinejs", "kind": "waline", "path": "tieba"},
    ]},
    {"id": "zhihu", "name": "知乎", "icon": "zhihu_知乎益蜂", "sets": [
        {"id": "zhihu", "name": "知乎", "prefix": "zhihu_", "source": "bilicon", "kind": "iconify", "path": "json/zhihu.json"},
    ]},
    {"id": "rednote", "name": "小红书", "icon": "xhs_红书", "sets": [
        # 源数据的名称大多带 R 或 H 后缀（如「微笑R」），去掉后作为名称
        {"id": "rednote", "name": "小红书", "prefix": "xhs_", "source": "bilicon", "kind": "iconify", "path": "json/rednote.json",
         "strip": r"(?<=.)[RH]$"},
    ]},
    {"id": "dingtalk", "name": "钉钉", "icon": "dingtalk_微笑", "sets": [
        {"id": "dingtalk", "name": "钉钉", "prefix": "dingtalk_", "source": "stickers", "kind": "owo", "path": "钉钉"},
    ]},
    {"id": "qq", "name": "QQ", "icon": "qq_跳跳", "sets": [
        {"id": "qq", "name": "小黄脸", "prefix": "qq_", "source": "stickers", "kind": "owo", "path": "QQ-小黄脸表情"},
        {"id": "qq-face", "name": "黄脸", "prefix": "qqface_", "source": "stickers", "kind": "owo", "path": "QQ-QQ黄脸"},
        {"id": "qq-emoji", "name": "emoji", "prefix": "qqemoji_", "source": "stickers", "kind": "owo", "path": "QQ-emoji表情"},
        {"id": "qq-classic", "name": "经典", "prefix": "qqclassic_", "source": "walinejs", "kind": "waline", "path": "qq"},
    ]},
]

# 和前端解析器一致：code 里不能有空白和冒号，长度有上限
CODE_RE = re.compile(r"^[^\s:]{1,64}$")
IMAGE_EXTS = {".png", ".gif", ".webp", ".jpg", ".jpeg"}
DATA_URI_RE = re.compile(r'href="data:image/[a-z+]+;base64,([A-Za-z0-9+/=]+)"')
OWO_SRC_RE = re.compile(r'src="[^"]*?/Stickers/([^"]+)"')
MAX_SOURCE_BYTES = 4 * 1024 * 1024
MIN_FRAME_MS = 20
DEFAULT_FRAME_MS = 100


@dataclass
class RawItem:
    name: str
    data: bytes


def warn(msg: str) -> None:
    print(f"warning: {msg}", file=sys.stderr)


def read_file(root: Path, rel: str) -> bytes | None:
    """读取源仓库内的文件；路径越界、符号链接、过大或不存在时返回 None。"""
    path = root / rel
    try:
        resolved = path.resolve(strict=True)
    except OSError:
        return None
    if path.is_symlink() or not resolved.is_relative_to(root.resolve()) or not resolved.is_file():
        return None
    if resolved.stat().st_size > MAX_SOURCE_BYTES:
        warn(f"skip oversized file {rel}")
        return None
    return resolved.read_bytes()


def load_waline(root: Path, spec: dict) -> list[RawItem]:
    info = json.loads((root / spec["path"] / "info.json").read_text(encoding="utf-8"))
    items = []
    for name in info["items"]:
        data = read_file(root, f'{spec["path"]}/{info["prefix"]}{name}.{info["type"]}')
        if data is None:
            warn(f'{spec["id"]}: missing {name}')
            continue
        items.append(RawItem(name, data))
    return items


def load_owo(root: Path, spec: dict) -> list[RawItem]:
    owo = json.loads((root / "OwO.json").read_text(encoding="utf-8"))
    items = []
    for entry in owo[spec["path"]]["container"]:
        m = OWO_SRC_RE.search(entry["icon"])
        data = read_file(root, m.group(1)) if m else None
        if data is None:
            warn(f'{spec["id"]}: missing {entry.get("text")}')
            continue
        items.append(RawItem(entry["text"], data))
    return items


def load_iconify(root: Path, spec: dict) -> list[RawItem]:
    icons = json.loads((root / spec["path"]).read_text(encoding="utf-8"))["icons"]
    items = []
    for name, icon in icons.items():
        m = DATA_URI_RE.search(icon["body"])
        if not m:
            warn(f'{spec["id"]}: no embedded image for {name}')
            continue
        items.append(RawItem(name, base64.b64decode(m.group(1))))
    return items


LOADERS = {"waline": load_waline, "owo": load_owo, "iconify": load_iconify}


def encode_still(frame: Image.Image) -> bytes:
    # 小尺寸表情用有损 q85 看不出差别，体积约为无损的三分之一
    out = io.BytesIO()
    frame.save(out, "WEBP", quality=85, method=6)
    return out.getvalue()


def pick_still(frames: list[Image.Image]) -> Image.Image:
    """动图的静态帧：取第一个不透明面积达到最大值一半的帧，避开从空白淡入的开头。"""
    coverage = [ImageStat.Stat(f.getchannel("A")).sum[0] for f in frames]
    best = max(coverage)
    return next(f for f, c in zip(frames, coverage) if c >= best / 2)


def convert(data: bytes, size: int) -> tuple[bytes, bytes | None]:
    """转成最长边不超过 size 的 WebP，返回（图片，动图的静态帧）。静态图第二项为 None。

    动图有损压缩也只能比 APNG 原图小两三成，所以另出一张静态帧：表情面板只加载静态帧，
    消息里才显示动图，减少动态效果时也用静态帧。
    """
    with Image.open(io.BytesIO(data)) as im:
        frames, durations = [], []
        for frame in ImageSequence.Iterator(im):
            f = frame.convert("RGBA")
            if max(f.size) > size:
                f.thumbnail((size, size), Image.Resampling.LANCZOS)
            frames.append(f)
            # 浏览器会把过短的帧时长按 100ms 处理，这里提前统一，避免不同浏览器播放速度不一致
            ms = frame.info.get("duration") or 0
            durations.append(int(ms) if ms >= MIN_FRAME_MS else DEFAULT_FRAME_MS)
    if len(frames) == 1:
        return encode_still(frames[0]), None
    out = io.BytesIO()
    # minimize_size 能再小一成多，但编码时间要翻好几倍，不值得
    frames[0].save(out, "WEBP", save_all=True, append_images=frames[1:], duration=durations, loop=0,
                   quality=75, method=4)
    return out.getvalue(), encode_still(pick_still(frames))


def check_source(root: Path, key: str) -> None:
    """源仓库提交和 SOURCES 不一致时只提醒，不中断：内容以实际读到的为准。"""
    head = root / ".git" / "HEAD"
    if not head.is_file():
        warn(f"{key}: not a git checkout, cannot verify commit")
        return
    ref = head.read_text(encoding="utf-8").strip()
    commit = ref
    if ref.startswith("ref: "):
        ref_file = root / ".git" / ref[5:]
        commit = ref_file.read_text(encoding="utf-8").strip() if ref_file.is_file() else ""
    if commit != SOURCES[key]["commit"]:
        warn(f'{key}: checkout is {commit or "unknown"}, expected {SOURCES[key]["commit"]}')


def prepare_out(out: Path) -> None:
    """只清理本脚本生成过的目录，避免参数写错时删掉别的东西。"""
    if out.exists():
        manifest = out / "manifest.json"
        ours = False
        if manifest.is_file():
            try:
                ours = json.loads(manifest.read_text(encoding="utf-8")).get("generator") == GENERATOR
            except (ValueError, OSError):
                ours = False
        if any(out.iterdir()) and not ours:
            sys.exit(f"error: {out} is not empty and was not generated by this script")
        shutil.rmtree(out)
    out.mkdir(parents=True)


def build(sources: Path, out: Path, size: int) -> dict:
    for key, src in SOURCES.items():
        check_source(sources / src["dir"], key)

    prepare_out(out)
    seen: set[str] = set()
    platforms, rows = [], []
    total_items, total_bytes = 0, 0
    for p in PLATFORMS:
        sets = []
        for spec in p["sets"]:
            root = sources / SOURCES[spec["source"]]["dir"]
            (out / spec["id"]).mkdir()
            items = []
            for raw in LOADERS[spec["kind"]](root, spec):
                name = re.sub(spec["strip"], "", raw.name) if "strip" in spec else raw.name
                code = spec["prefix"] + name
                if not CODE_RE.match(code):
                    warn(f"{spec['id']}: invalid code {code!r}, skipped")
                    continue
                if code in seen:
                    warn(f"{spec['id']}: duplicate code {code!r}, skipped")
                    continue
                try:
                    webp, still = convert(raw.data, size)
                except (OSError, ValueError) as e:
                    warn(f"{spec['id']}: cannot decode {name!r}: {e}")
                    continue
                seen.add(code)
                stem = f"{spec['id']}/{len(items) + 1:03d}"
                item = {"code": code, "name": name, "file": f"{stem}.webp"}
                (out / item["file"]).write_bytes(webp)
                total_bytes += len(webp)
                if still is not None:
                    item["still"] = f"{stem}.still.webp"
                    (out / item["still"]).write_bytes(still)
                    total_bytes += len(still)
                items.append(item)
            sets.append({"id": spec["id"], "name": spec["name"], "items": items})
            src = SOURCES[spec["source"]]
            rows.append(f'| {p["name"]} | {spec["name"]} | {len(items)} | `{spec["prefix"]}` | {src["repo"]} `{spec["path"]}` |')
            total_items += len(items)
            print(f"{spec['id']}: {len(items)}")
        all_items = [i for s in sets for i in s["items"]]
        icon = next((i for i in all_items if i["code"] == p["icon"]), all_items[0] if all_items else None)
        platforms.append({"id": p["id"], "name": p["name"], "icon": icon.get("still", icon["file"]) if icon else "", "sets": sets})

    manifest = {"generator": GENERATOR, "version": MANIFEST_VERSION, "size": size, "platforms": platforms}
    (out / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
    (out / "README.md").write_text(readme(rows, size), encoding="utf-8")
    print(f"total: {total_items} items, {total_bytes / 1024 / 1024:.1f} MB -> {out}")
    return manifest


def readme(rows: list[str], size: int) -> str:
    sources = "\n".join(f'| {v["repo"]} | `{v["commit"][:12]}` | {v["license"]} |' for v in SOURCES.values())
    return f"""# 平台表情资源包

微信、抖音、B站、贴吧、知乎、小红书、钉钉、QQ 的小表情（不含表情包），由 MarsChat 的 `scripts/emoji_packs.py` 从开源仓库提取生成。
所有图片统一为 WebP，最长边不超过 {size}px。动图另有一张静态帧（`*.still.webp`），体积只有动图的几十分之一。

## 版权

表情图片的版权归各平台所有。来源仓库的开源协议只覆盖仓库本身，不代表获得了平台的授权。
仅供个人学习和非商业项目使用；公开部署或商用前请自行确认授权。不要把本目录提交到公开仓库。

## 内容

| 平台 | 子集 | 数量 | code 前缀 | 来源 |
|---|---|---|---|---|
{chr(10).join(rows)}

| 来源仓库 | 提交 | 协议 |
|---|---|---|
{sources}

## manifest.json

```jsonc
{{
  "generator": "{GENERATOR}",
  "version": {MANIFEST_VERSION},
  "size": {size},
  "platforms": [
    {{
      "id": "qq", "name": "QQ", "icon": "qq/001.still.webp",
      "sets": [
        {{ "id": "qq", "name": "小黄脸",
          "items": [{{ "code": "qq_微笑", "name": "微笑", "file": "qq/001.webp", "still": "qq/001.still.webp" }}] }}
      ]
    }}
  ]
}}
```

- `platforms` 的顺序就是建议的展示顺序；一个平台可以有多个子集（如 QQ 的小黄脸、黄脸、emoji、经典）。
- `code` 全局唯一，只含非空白、非冒号字符；`file`、`still`、`icon` 都是相对本目录的路径。
- 只有动图才有 `still`。建议选择面板里显示 `still ?? file`，消息里显示 `file`，「减少动态效果」时显示 `still`。

## 用法

消息正文里写 `:code:`（如 `:wechat_微笑:`），客户端加载 manifest 后把认识的 code 渲染成图片，不认识的保持原文。
这样不需要后端改动，没有资源包的客户端也能看到可读的文字。

```js
const base = "/emoji-packs"
const manifest = await fetch(`${{base}}/manifest.json`).then((r) => r.json())
const byCode = new Map(manifest.platforms.flatMap((p) => p.sets.flatMap((s) => s.items)).map((i) => [i.code, i]))
const html = text.replace(/:([^\\s:]{{1,64}}):/g, (m, code) => {{
  const item = byCode.get(code)
  return item ? `<img src="${{base}}/${{item.file}}" alt="${{item.name}}">` : m
}})
// 注意：示例未处理 HTML 转义，也没有处理「:未知:已知:」这种相邻冒号，正式使用时按需完善
```
"""


def main() -> None:
    parser = argparse.ArgumentParser(description="extract platform emoji packs")
    parser.add_argument("--sources", type=Path, default=Path("tmp/emoji-sources"))
    parser.add_argument("--out", type=Path, default=Path("web/public/emoji-packs"))
    parser.add_argument("--size", type=int, default=128)
    args = parser.parse_args()
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
    build(args.sources, args.out, args.size)


if __name__ == "__main__":
    main()
