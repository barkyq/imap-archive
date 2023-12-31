package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"strings"
)

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
				var red io.ReadCloser
				if f, e := os.Open(strings.TrimSpace(path)); e == nil {
					red = f
				} else if g, e := os.Open(strings.TrimSpace(path) + ".gz"); e != nil {
					g.Close()
					f.Close()
					continue
				} else if r, e := gzip.NewReader(g); e != nil {
					panic(e)
				} else {
					red = r
				}
				if msg, err := mail.ReadMessage(red); err == nil {
					fmt.Fprintf(wp, "%s id:%s\n", tags_joined, strings.Trim(msg.Header.Get("Message-ID"), "<>"))
				} else {
					panic(err)
				}
				red.Close()
			}
		}
		wp.Close()
	}()
	return cmd.Run()
}
