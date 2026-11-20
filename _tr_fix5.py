import io
path = r"D:\code\mpi\argENT-go\internal\session\store.go"
with io.open(path, "r", encoding="utf-8", newline="") as f:
    text = f.read()
old = "// Container returns the directory handle behind the store."
new = "// Container 返回 store 背后的目录句柄。"
print(text.count(old))
if text.count(old) == 1:
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text.replace(old, new, 1))
