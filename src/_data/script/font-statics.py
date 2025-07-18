#!/usr/bin/env python3
import fontforge
import sys
import unicodedata
from collections import defaultdict

if len(sys.argv) < 2:
    print("Usage: fontforge -script font_unicode_report.py fontfile.ttf")
    sys.exit(1)

font_path = sys.argv[1]
font = fontforge.open(font_path)

# 收集所有有效碼位
codepoints = sorted([g.unicode for g in font.glyphs() if g.unicode != -1])

# 定義 Unicode 區塊
blocks = {
    "Basic Latin": (0x0000, 0x007F),
    "Latin extend Supplement": (0x0080, 0x24F),
    "CJK Symbols and Punctuation": (0x3000, 0x303F),
    "Hiragana": (0x3040, 0x309F),
    "Katakana": (0x30A0, 0x30FF),
    "Bopomofo": (0x3100, 0x312F),
    "Hangul Syllables": (0xAC00, 0xD7AF),
    "CJK Unified Ideographs": (0x4E00, 0x9FFF),
    "CJK Compatibility Ideographs": (0xF900, 0xFAFF),
    "Arabic": (0x0600, 0x06FF),
    "Private Use Area (PUA)": (0xE000, 0xF8FF),
    "PUA-A (Plane 15)": (0xF0000, 0xFFFFD),
    "PUA-B (Plane 16)": (0x100000, 0x10FFFD),
    "Specials": (0xFFF0, 0xFFFF)
}

# 依區塊統計
block_stats = {}
block_glyphs = defaultdict(list)

for name, (start, end) in blocks.items():
    in_block = [cp for cp in codepoints if start <= cp <= end]
    total = end - start + 1
    block_stats[name] = (len(in_block), total)
    block_glyphs[name].extend(in_block)

# 未知區塊收集
others = []
for cp in codepoints:
    if not any(start <= cp <= end for start, end in blocks.values()):
        others.append(cp)

if others:
    block_stats["Other (Unmapped)"] = (len(others), len(others))
    block_glyphs["Other (Unmapped)"].extend(others)

# ======== 輸出報表 ========
print(f"Font: {font_path}")
print("=" * 60)
for name, (count, total) in block_stats.items():
    percent = (count / len(codepoints)) * 100
    total_reg+=percent
    print(f"{name:40s}: {count:5d}/{total:<6d} ({percent:5.1f}%)")
# print("\n=== 詳細清單 (每個區塊用 U+XXXX 表示) ===")
# for name, cps in block_glyphs.items():
#     if not cps:
#         continue
#     print(f"\n{name} ({len(cps)} glyphs):")
#     print(", ".join(f"U+{cp:04X}" for cp in cps))
#
