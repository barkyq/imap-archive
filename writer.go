package main

import (
	"bufio"
	"fmt"
	"io"
	"net/mail"
)

func WriteMessage(headers mail.Header, rb *bufio.Reader, w io.Writer) (n int, e error) {
	for _, h := range header_list {
		if v := headers.Get(h); v != "" {
			if k, e := fmt.Fprintf(w, "%s: %s\n", h, v); e != nil {
				return n + k, e
			} else {
				n += k
			}
		}
	}
	if rb == nil {
		return
	}
	w.Write([]byte{'\n'})
	n++
	for {
		if b, isPrefix, e := rb.ReadLine(); e == io.EOF || isPrefix {
			if k, e := w.Write(b); e != nil {
				return n + k, e
			} else {
				n += k
			}
			if e == io.EOF {
				break
			} else {
				continue
			}
		} else if e != nil {
			panic(e)
		} else if !isPrefix {
			if k, e := w.Write(b); e != nil {
				return n + k, e
			} else {
				w.Write([]byte{'\n'})
				n += k + 1
			}
		} else {
			return n, fmt.Errorf("error writing message")
		}

	}
	return n, nil
}

func WriteHeaders(headers mail.Header, w io.Writer) (n int, e error) {
	for _, h := range canonical_header_list {
		if v := headers.Get(h); v != "" {
			if k, e := fmt.Fprintf(w, "%s: %s\n", h, v); e != nil {
				return n + k, e
			} else {
				n += k
			}
		}
	}
	return n, nil
}

var canonical_header_list = []string{
	"From",
	"Date",
	"Message-ID",
}

var header_list = []string{
	"From",
	"To",
	"Cc",
	"Subject",
	"In-Reply-To",
	"References",
	"Date",
	"Message-ID",
	"MIME-Version",
	"Content-Type",
	"Content-Disposition",
	"Content-Transfer-Encoding",
	"Hash",
}
