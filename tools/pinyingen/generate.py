#!/usr/bin/env python3
"""Regenerate internal/types/pinyin_table.txt (KnowAxis V3.0 §12.1 容错).

为什么用生成而不是引依赖：
  - 仓库不能访问网络，拼音数据没法在构建时拉；
  - 一个第三方拼音库通常带自己的词典与分词逻辑，而我们只需要"汉字 → 无调
    音节"这一张表，用来把中文查询与拼音输入对上；
  - ICU 的 Han-Latin 转写是系统自带的权威数据，生成一次即可复现。

用法：
    python3 tools/pinyingen/generate.py > internal/types/pinyin_table.txt

需要 uconv（ICU 的命令行工具，Debian/Ubuntu 在 icu-devtools 包内）。
表格式：每行 "<音节>:<汉字...>"，音节按字典序，汉字按码位排序；只收
GB2312 常用字（6763 个），足够覆盖制度/设备/工艺术语，也让表保持在 22KB。
一个汉字可能出现在多个音节下（多音字），查询时按"任一读音命中"处理。
"""

import subprocess
import sys
from collections import defaultdict

# GB2312 覆盖的汉字区间；超出部分的生僻字不参与拼音召回。
CJK_START, CJK_END = 0x4E00, 0x9FA6

# 两种转写各取一次：Han-Latin 给常用读音，Han-Latin/Names 给姓名场景读音。
# 多音字（重/长/乐/单/曾/解 …）因此可以同时被两种读法搜到。
TRANSLITERATIONS = (
    "Han-Latin; Latin-ASCII; Lower",
    "Han-Latin/Names; Latin-ASCII; Lower",
)


def transliterate(chars: list[str], transform: str) -> list[str]:
    result = subprocess.run(
        ["uconv", "-x", transform],
        input="\n".join(chars),
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout.split("\n")


def main() -> int:
    chars = [chr(code) for code in range(CJK_START, CJK_END)]
    readings = [transliterate(chars, transform) for transform in TRANSLITERATIONS]

    groups: dict[str, set[str]] = defaultdict(set)
    for index, char in enumerate(chars):
        try:
            char.encode("gb2312")
        except UnicodeEncodeError:
            continue  # 生僻字：不参与拼音召回
        for readings_per_char in readings:
            raw = readings_per_char[index].strip()
            # 逐字转写在理论上可能给出多个音节；全部收录，查询侧取并集。
            for syllable in raw.replace(",", " ").split():
                if syllable.isalpha() and 1 <= len(syllable) <= 8:
                    groups[syllable].add(char)

    for syllable in sorted(groups):
        sys.stdout.write(syllable + ":" + "".join(sorted(groups[syllable])) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
