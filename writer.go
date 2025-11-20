package main

import (
	"fmt"
	"io"
	"net/mail"
)

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
