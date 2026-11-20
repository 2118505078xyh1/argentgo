import io
BASE = r"D:\code\mpi\argENT-go\internal\session"

def ind(block, n):
    p = "\t" * n
    return "\n".join(p + line for line in block.split("\n"))

path = BASE + "\\retention.go"
with io.open(path, "r", encoding="utf-8", newline="") as f:
    text = f.read()
old = ind(r"""// json.Unmarshal(null, &string) succeeds with an empty string; a null hint
// means "no hint", so the automatic default still applies.""", 1)
new = ind(r"""// json.Unmarshal(null, &string) 会以空字符串成功返回；null 提示表示"无提示"，
// 因此仍适用自动默认值。""", 1)
n = text.count(old)
print("count", n)
if n == 1:
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text.replace(old, new, 1))
    print("OK")
