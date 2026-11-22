package bash

// 本文件把并行实现的扩展内建命令统一注册到内建表与分派入口。
// 各命令实现分散在 coreutils_fs2.go / coreutils_sys.go / coreutils_sys2.go /
// coreutils_text2.go / coreutils_net.go / coreutils_archive.go 中，这里只做接线。

// extraBuiltinNames 是除 shellBuiltinNames/coreutilNames/sysBuiltinNames 之外
// 新增的命令名。isBuiltin 会查询它。
var extraBuiltinNames = map[string]bool{
	// 文件/目录/查找（coreutils_fs2.go）
	"rmdir": true, "ln": true, "chmod": true, "chown": true, "chgrp": true,
	"umask": true, "find": true, "whereis": true, "locate": true,
	"basename": true, "dirname": true,
	// 系统信息/进程（coreutils_sys.go）
	"uname": true, "hostname": true, "date": true, "whoami": true, "id": true,
	"ps": true, "kill": true, "killall": true, "pkill": true,
	"df": true, "du": true, "free": true, "uptime": true,
	"who": true, "w": true, "dmesg": true, "top": true, "htop": true,
	"jobs": true, "fg": true, "bg": true, "nohup": true,
	// 文本处理（coreutils_text2.go）
	"sed": true, "awk": true, "xargs": true, "sort": true, "uniq": true,
	"cut": true, "tr": true, "less": true, "more": true, "nl": true,
	// 网络（coreutils_net.go）
	"curl": true, "wget": true, "ping": true, "ss": true, "netstat": true,
	"ip": true, "ifconfig": true, "dig": true, "nslookup": true,
	"ssh": true, "scp": true, "rsync": true,
	// 压缩打包（coreutils_archive.go）
	"tar": true, "zip": true, "unzip": true, "gzip": true, "gunzip": true,
}

// handled3 把三返回值命令实现适配为 runExtraBuiltin 的四返回值契约。
func handled3(a, b string, c error) (string, string, error, bool) { return a, b, c, true }

// runExtraBuiltin 分派 extraBuiltinNames 中的命令。
// 返回 handled=false 表示 name 不属于本文件，调用方应继续其它分支。
func (it *Interp) runExtraBuiltin(name string, args []string, stdin string) (string, string, error, bool) {
	if !extraBuiltinNames[name] {
		return "", "", nil, false
	}
	switch name {
	// —— coreutils_fs2.go（两参数实现）——
	case "rmdir":
		a, b, c := it.biRmdir(args)
		return a, b, c, true
	case "ln":
		a, b, c := it.biLn(args)
		return a, b, c, true
	case "chmod":
		a, b, c := it.biChmod(args)
		return a, b, c, true
	case "chown":
		a, b, c := it.biChown(args)
		return a, b, c, true
	case "chgrp":
		a, b, c := it.biChgrp(args)
		return a, b, c, true
	case "umask":
		a, b, c := it.biUmask(args)
		return a, b, c, true
	case "find":
		a, b, c := it.biFind(args)
		return a, b, c, true
	case "whereis":
		a, b, c := it.biWhereis(args)
		return a, b, c, true
	case "locate":
		a, b, c := it.biLocate(args)
		return a, b, c, true
	case "basename":
		a, b, c := it.biBasename(args)
		return a, b, c, true
	case "dirname":
		a, b, c := it.biDirname(args)
		return a, b, c, true
	// —— coreutils_sys.go（三参数实现）——
	case "uname":
		return handled3(it.biUname(args, stdin))
	case "hostname":
		return handled3(it.biHostname(args, stdin))
	case "date":
		return handled3(it.biDate(args, stdin))
	case "whoami":
		return handled3(it.biWhoami(args, stdin))
	case "id":
		return handled3(it.biID(args, stdin))
	case "ps":
		return handled3(it.biPS(args, stdin))
	case "kill":
		return handled3(it.biKill(args, stdin))
	case "killall":
		return handled3(it.biKillall(args, stdin))
	case "pkill":
		return handled3(it.biPkill(args, stdin))
	case "df":
		return handled3(it.biDf(args, stdin))
	case "du":
		return handled3(it.biDu(args, stdin))
	case "free":
		return handled3(it.biFree(args, stdin))
	case "uptime":
		return handled3(it.biUptime(args, stdin))
	case "who":
		return handled3(it.biWho(args, stdin))
	case "w":
		return handled3(it.biW(args, stdin))
	case "dmesg":
		return handled3(it.biDmesg(args, stdin))
	case "top":
		return handled3(it.biTop(args, stdin))
	case "htop":
		return handled3(it.biHtop(args, stdin))
	case "jobs":
		return handled3(it.biJobs(args, stdin))
	case "fg":
		return handled3(it.biFg(args, stdin))
	case "bg":
		return handled3(it.biBg(args, stdin))
	case "nohup":
		return handled3(it.biNohup(args, stdin))
	// —— coreutils_text2.go ——
	case "sed":
		return handled3(it.biSed(args, stdin))
	case "awk":
		return handled3(it.biAwk(args, stdin))
	case "xargs":
		return handled3(it.biXargs(args, stdin))
	case "sort":
		return handled3(it.biSort(args, stdin))
	case "uniq":
		return handled3(it.biUniq(args, stdin))
	case "cut":
		return handled3(it.biCut(args, stdin))
	case "tr":
		return handled3(it.biTr(args, stdin))
	case "less":
		return handled3(it.biLess(args, stdin))
	case "more":
		return handled3(it.biMore(args, stdin))
	case "nl":
		return handled3(it.biNl(args, stdin))
	// —— coreutils_net.go ——
	case "curl":
		return handled3(it.biCurl(args, stdin))
	case "wget":
		return handled3(it.biWget(args, stdin))
	case "ping":
		return handled3(it.biPing(args, stdin))
	case "ss":
		return handled3(it.biSs(args, stdin))
	case "netstat":
		return handled3(it.biNetstat(args, stdin))
	case "ip":
		return handled3(it.biIp(args, stdin))
	case "ifconfig":
		return handled3(it.biIfconfig(args, stdin))
	case "dig":
		return handled3(it.biDig(args, stdin))
	case "nslookup":
		return handled3(it.biNslookup(args, stdin))
	case "ssh":
		return handled3(it.biSsh(args, stdin))
	case "scp":
		return handled3(it.biScp(args, stdin))
	case "rsync":
		return handled3(it.biRsync(args, stdin))
	// —— coreutils_archive.go ——
	case "tar":
		return handled3(it.biTar(args, stdin))
	case "zip":
		a, b, c := it.biZip(args)
		return a, b, c, true
	case "unzip":
		a, b, c := it.biUnzip(args)
		return a, b, c, true
	case "gzip":
		return handled3(it.biGzip(args, stdin))
	case "gunzip":
		return handled3(it.biGunzip(args, stdin))
	}
	return "", "", nil, false
}
