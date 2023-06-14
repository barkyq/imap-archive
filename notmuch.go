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
	"strconv"
	"strings"
)

func Revision(file string) (revision string, antirevision string, e error) {
	cmd := exec.Command("notmuch", "count", "--lastmod")
	rp, wp := io.Pipe()
	cmd.Stdout = wp
	go cmd.Run()
	new_uuid, _, e := readLastModFile(rp)
	if e != nil {
		return "..", "..", e
	}
	if f, e := os.Open(file); e != nil {
		return "..", "..", e
	} else if uuid, lastmod, e := readLastModFile(f); e != nil {
		return "..", "..", nil
	} else if uuid != new_uuid {
		return "..", "..", nil
	} else {
		return fmt.Sprintf("%d..", lastmod), fmt.Sprintf("..%d", lastmod), nil
	}
}

func SaveLastModFile(file string) (e error) {
	cmd := exec.Command("notmuch", "count", "--lastmod")
	if f, e := os.CreateTemp(filepath.Dir(file), ".lastmod"); e != nil {
		return e
	} else {
		cmd.Stdout = f
		if e := cmd.Run(); e != nil {
			return e
		} else if e := f.Close(); e != nil {
			return e
		} else {
			return os.Rename(f.Name(), file)
		}
	}
}

func readLastModFile(r io.Reader) (uuid string, lastmod int, e error) {
	var line [128]byte
	if n, e := r.Read(line[:]); e != nil {
		return uuid, lastmod, e
	} else if arr := strings.Split(fmt.Sprintf("%s", line[:n-1]), fmt.Sprintf("%s", []byte{0x09})); len(arr) != 3 {
		return uuid, lastmod, fmt.Errorf("notmuch error")
	} else if i, e := strconv.ParseInt(strings.TrimSpace(arr[2]), 10, 64); e != nil {
		return uuid, int(i), e
	} else {
		return strings.TrimSpace(arr[1]), int(i), nil
	}
}

func NotmuchTag(tag string, rev string) (has_tag [][digest_length - 1]byte, err error) {
	query := fmt.Sprintf("tag:%s lastmod:%s", tag, rev)
	rp, wp := io.Pipe()
	count, e := func() (uint64, error) {
		var b [12]byte
		rp, wp := io.Pipe()
		cmd := exec.Command("notmuch", "count", query)
		cmd.Stdout = wp
		go cmd.Run()
		if n, e := rp.Read(b[:]); e != nil {
			return 0, e
		} else {
			return strconv.ParseUint(fmt.Sprintf("%s", b[:n-1]), 10, 64)
		}
	}()
	if e != nil {
		return nil, e
	}
	rb := bufio.NewReader(rp)
	cmd := exec.Command("notmuch", "search", "--output=files", query)
	cmd.Stdout = wp
	go func() {
		if e := cmd.Run(); e != nil {
			fmt.Fprintln(os.Stderr, "notmuch search error! continuing...")
			wp.Close()
		} else {
			wp.Close()
		}
	}()
	has_tag = make([][digest_length - 1]byte, 0, count)
	for {
		if s, e := rb.ReadString('\n'); e != nil {
			break
		} else if b, e := hex.DecodeString(filepath.Base(s)[:38]); e != nil || len(b) != digest_length-1 {
			panic(e)
		} else {
			var tmp [digest_length - 1]byte
			copy(tmp[:], b)
			has_tag = append(has_tag, tmp)
		}
	}
	if len(has_tag) == 0 {
		return nil, nil
	}
	sort.Slice(has_tag, func(i int, j int) bool {
		for k, b := range has_tag[i] {
			switch {
			case b < has_tag[j][k]:
				return true
			case b == has_tag[j][k]:
				continue
			case b > has_tag[j][k]:
				return false
			}
		}
		return false
	})
	return has_tag, nil
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
		for {
			if line, e := pathbuffer.ReadString('\n'); e != nil {
				break
			} else {
				arr := strings.Split(line, " ")
				tags := arr[:len(arr)-1]
				path := arr[len(arr)-1]
				tags_joined := strings.Join(tags, " ")
				if f, e := os.Open(strings.TrimSpace(path)); e != nil {
					panic(e)
				} else {
					if msg, err := mail.ReadMessage(f); err == nil {
						fmt.Fprintf(wp, "%s id:%s\n", tags_joined, strings.Trim(msg.Header.Get("Message-ID"), "<>"))
					} else {
						panic(err)
					}
					f.Close()
				}
			}
		}
		wp.Close()
	}()
	return cmd.Run()
}
