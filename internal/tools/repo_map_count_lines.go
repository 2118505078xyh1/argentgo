package tools

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// countLinesChunkSize 是流式统计行数时的单次读取分块大小。
// 它同时作为 bufio 的内部缓冲区大小和临时切片大小，
// 使整个过程的内存占用保持为常量，与输入文件的大小无关。
const countLinesChunkSize = 64 * 1024

// countLinesStreaming 流式统计 r 中的行数。它按固定大小分块读取并只统计
// 换行符字节 '\n'，不会把整个输入读入内存，也不会解码 UTF-8：
// 多字节字符（包括 CJK）的内部字节永远不会等于 '\n'，
// 因此分块边界即使切开了一个字符也不影响计数。
//
// 行数的口径与原 countLines 保持一致：结尾的换行符视为终止最后一行，
// 所以 "a\nb\n" 与 "a\nb" 都报告 2 行；空输入报告 0 行。
// 读取过程中若出现非 EOF 错误，返回 (0, err)，调用方不应使用部分计数。
func countLinesStreaming(r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, countLinesChunkSize)
	chunk := make([]byte, countLinesChunkSize)
	newline := []byte{'\n'}

	lines := 0
	var last byte
	readAny := false
	for {
		n, err := br.Read(chunk)
		if n > 0 {
			part := chunk[:n]
			lines += bytes.Count(part, newline)
			last = part[len(part)-1]
			readAny = true
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}
	}
	// 最后一次读取没有以换行符结尾时，末尾还有一行尚未被计入。
	if readAny && last != '\n' {
		lines++
	}
	return lines, nil
}
