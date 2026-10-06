// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hooknote

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func chunked(body string) string {
	return fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
}

func TestReadHTTPSupportsChunkedAndBoundedBodies(t *testing.T) {
	for _, n := range []int{3000, 16 << 10} {
		body := strings.Repeat("a", n)
		raw := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: keep-alive\r\n\r\n" + chunked(body)
		length := "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n"
		reader := bufio.NewReader(strings.NewReader(raw + length))
		status, got, err := readHTTP(reader)
		if err != nil || status != 200 || string(got) != body {
			t.Fatalf("chunked %d: status %d err %v len %d", n, status, err, len(got))
		}
		status, got, err = readHTTP(reader)
		if err != nil || status != 204 || len(got) != 0 {
			t.Fatalf("following response: status %d err %v len %d", status, err, len(got))
		}
	}
	over := strings.Repeat("b", (16<<10)+1)
	raw := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n" + chunked(over)
	if _, _, err := readHTTP(bufio.NewReader(strings.NewReader(raw))); err != ErrLimit {
		t.Fatal("oversized chunked body", err)
	}
}
