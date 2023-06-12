package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func NotmuchUnread() (unread [][digest_length - 1]byte, err error) {
	cmd := exec.Command("notmuch", "search", "--output=files", "(tag:unread)")
	rp, wp := io.Pipe()
	rb := bufio.NewReader(rp)
	cmd.Stdout = wp
	go func() {
		if e := cmd.Run(); e != nil {
			fmt.Fprintln(os.Stderr, "notmuch search error! continuing...")
			wp.Close()
		} else {
			wp.Close()
		}
	}()
	unread = make([][digest_length - 1]byte, 0, 128)
	for {
		if s, e := rb.ReadString('\n'); e != nil {
			break
		} else if b, e := hex.DecodeString(filepath.Base(s)[:38]); e != nil || len(b) != digest_length-1 {
			panic(e)
		} else {
			var tmp [digest_length - 1]byte
			copy(tmp[:], b)
			unread = append(unread, tmp)
		}
	}
	if len(unread) == 0 {
		return nil, nil
	}
	sort.Slice(unread, func(i int, j int) bool {
		for k, b := range unread[i] {
			switch {
			case b < unread[j][k]:
				return true
			case b == unread[j][k]:
				continue
			case b > unread[j][k]:
				return false
			}
		}
		return false
	})
	return unread, nil
}

func UpdateNotmuch(pathbuffer *bytes.Buffer) error {
	update := exec.Command("notmuch", "new")
	if e := update.Run(); e != nil {
		panic(e)
	}

	cmd := exec.Command("notmuch", "tag", "--batch")
	rp, wp := io.Pipe()
	cmd.Stdin = rp
	go func() {
		if e := cmd.Run(); e != nil {
			panic(e)
		}
	}()
	for {
		if line, e := pathbuffer.ReadString('\n'); e != nil {
			break
		} else if f, e := os.Open(strings.TrimSpace(line)); e != nil {
			panic(e)
		} else {
			if msg, err := mail.ReadMessage(f); err == nil {
				fmt.Fprintf(wp, "-unread id:%s\n", strings.Trim(msg.Header.Get("Message-ID"), "<>"))
			} else {
				panic(err)
			}
			f.Close()
		}
	}
	return wp.Close()
}
