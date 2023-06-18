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
	"strconv"
	"strings"
)

func LastMod() (new_uuid string, last_mod int, e error) {
	cmd := exec.Command("notmuch", "count", "--lastmod")
	rp, wp := io.Pipe()
	cmd.Stdout = wp
	go cmd.Run()
	return readLastModFile(rp)
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

func GetNotmuchTags(taglist []string, buffer [][][digest_length - 1]byte, uuid string, lastmod int) ([][][digest_length - 1]byte, error) {
	var counter int
	if len(buffer) < len(taglist) {
		buffer = make([][][digest_length - 1]byte, len(taglist))
	}
	for k, tag := range taglist {
		var query []string
		if k == 0 {
			query = []string{fmt.Sprintf("tag:%s", tag)}
		} else {
			query = []string{fmt.Sprintf("--uuid=%s", uuid), fmt.Sprintf("tag:%s", tag), fmt.Sprintf("lastmod:%d..", lastmod)}
		}
		if count, e := func() (uint64, error) {
			var b [12]byte
			rp, wp := io.Pipe()
			q := make([]string, 1, 4)
			q[0] = "count"
			q = append(q, query...)
			cmd := exec.Command("notmuch", q...)
			cmd.Stdout = wp
			go func() {
				if e := cmd.Run(); e != nil {
					panic(e)
				}
			}()
			if n, e := rp.Read(b[:]); e != nil {
				panic(e)
			} else {
				return strconv.ParseUint(fmt.Sprintf("%s", b[:n-1]), 10, 64)
			}
		}(); e != nil {
			return nil, e
		} else if count == 0 {
			buffer[k] = buffer[k][:0]
			continue
		} else if cap(buffer[k]) < int(count)+8 {
			buffer[k] = make([][digest_length - 1]byte, 0, int(count)+16)
		} else {
			buffer[k] = buffer[k][:0]
		}
		if e := func() error {
			rp, wp := io.Pipe()
			rb := bufio.NewReader(rp)
			q := make([]string, 2, 5)
			q[0] = "search"
			q[1] = "--output=files"
			q = append(q, query...)

			cmd := exec.Command("notmuch", q...)
			cmd.Stdout = wp
			go func() {
				if e := cmd.Run(); e != nil {
					panic(e)
				} else {
					wp.Close()
				}
			}()
			var tmp [digest_length - 1]byte
			for {
				if s, e := rb.ReadString('\n'); e != nil {
					break
				} else if b, e := hex.DecodeString(filepath.Base(s)[:2*digest_length-2]); e != nil || len(b) != digest_length-1 {
					panic(e)
				} else {
					copy(tmp[:], b)
					counter++
					buffer[k] = append(buffer[k], tmp)
				}
			}
			return nil
		}(); e != nil {
			return nil, e
		}
	}
	if counter == 0 {
		return nil, nil
	} else {
		return buffer, nil
	}
}

func UpdateNotmuch(path_buffer *bytes.Buffer) error {
	if path_buffer.Len() == 0 {
		return nil
	}
	update := exec.Command("notmuch", "new")
	if e := update.Run(); e != nil {
		panic(e)
	}

	cmd := exec.Command("notmuch", "tag", "--batch")
	rp, wp := io.Pipe()
	cmd.Stdin = rp

	go func() {
		for {
			if line, e := path_buffer.ReadString('\n'); e == io.EOF {
				break
			} else if e != nil {
				panic(e)
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
						fmt.Println(f.Name())
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
