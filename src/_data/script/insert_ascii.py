# File: /home/iach526/Documents/myRepo/emfont/src/_data/script/insert_ascii.py

# Open a file to write the SQL commands
output_file = "insert_ascii.sql"

# with open(output_file, "w") as file:
#     for ascii_code in range(32, 127):  # ASCII printable characters
#         char = chr(ascii_code)
#         sql_command = f"INSERT INTO static_fonts(word, pack) VALUES ('{char}', 0);\n"
#         file.write(sql_command)

# Also include all full-width punctuation
full_width_punctuation = [
    '！', '＂', '＃', '＄', '％', '＆', '＇', '（', '）', '＊', '＋', '，', '－', '．', '／',
    '：', '；', '＜', '＝', '＞', '？', '＠', '［', '＼', '］', '＾', '＿', '｀', '｛', '｜', '｝', '～'
]

with open(output_file, "a") as file:  # Append to the same file
    for char in full_width_punctuation:
        sql_command = f"INSERT INTO static_fonts(word, pack) VALUES ('{char}', 1);\n"
        file.write(sql_command)

print(f"SQL commands have been written to {output_file}")