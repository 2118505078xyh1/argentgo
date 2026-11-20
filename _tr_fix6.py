import io
path = r"D:\code\mpi\argENT-go\internal\session\export_test.go"
with io.open(path, "r", encoding="utf-8", newline="") as f:
    text = f.read()
old = '\t\t{branch: "", want: "branch B", absent: "branch A"}, // active leaf'
new = '\t\t{branch: "", want: "branch B", absent: "branch A"}, // 活动叶节点'
print(text.count(old))
text = text.replace(old, new)
with io.open(path, "w", encoding="utf-8", newline="") as f:
    f.write(text)
