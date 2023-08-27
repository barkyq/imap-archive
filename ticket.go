package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var NULL_BYTES [digest_length]byte

type ArchiveTicket struct {
	hasher  hash.Hash
	batons  chan *ArchiveTicket
	tickets chan *ArchiveTicket
	digest  [digest_length]byte
	file    fs.File
	msg     *mail.Message
	rb      *bufio.Reader
	wb      *bufio.Writer
}

func (a *ArchiveTicket) Release() error {
	if a.file != nil {
		a.file.Close()
	}
	a.batons <- a
	return nil
}

func (a *ArchiveTicket) Submit() {
	a.Hash()
	a.tickets <- a
}

// make sure hasher is reset before calling Hash()
func (a *ArchiveTicket) Hash() error {
	if a.hasher == nil {
		a.hasher = sha256.New()
	}
	if _, e := WriteHeaders(a.msg.Header, a.hasher); e != nil {
		return e
	} else {
		copy(a.digest[:], a.hasher.Sum(nil))
	}
	a.hasher.Reset()
	return nil
}

func HandleArchiveTickets(targetdir string, tickets chan *ArchiveTicket) (int, error) {
	var first_byte string
	var rest_bytes string
	counterchan, sizes := make(chan int), make(chan int)
	go func() {
		var counter int
		for size := range sizes {
			counter += size
		}
		counterchan <- counter
	}()
	for ticket := range tickets {
		first_byte = fmt.Sprintf("%02x", ticket.digest[0])
		rest_bytes = fmt.Sprintf("%02x", ticket.digest[1:])
		if i, e := os.Stat(filepath.Join(targetdir, first_byte)); e != nil {
			if e := os.MkdirAll(filepath.Join(targetdir, first_byte), os.ModePerm); e != nil {
				ticket.Release()
				return 0, e
			}
		} else if !i.IsDir() {
			ticket.Release()
			return 0, fmt.Errorf("invalid target directory structure")
		}
		go func(ticket *ArchiveTicket, first_byte string, rest_bytes string) {
			defer ticket.Release()
			if g, e := os.CreateTemp("/tmp/", "tmp_message"); e != nil {
				panic(e)
			} else {
				ticket.wb.Reset(g)
				if n, e := WriteMessage(ticket.msg.Header, ticket.rb, ticket.wb); e != nil {
					panic(e)
				} else if e := ticket.wb.Flush(); e != nil {
					panic(e)
				} else if e := g.Close(); e != nil {
					panic(e)
				} else if e := os.Rename(g.Name(), filepath.Join(targetdir, first_byte, rest_bytes)); e != nil {
					panic(e)
				} else {
					sizes <- n
				}
			}
		}(ticket, first_byte, rest_bytes)
	}
	close(sizes)

	return <-counterchan, nil
}

type ResponseTicket struct {
	uid     uint32
	digest  [digest_length]byte
	headers mail.Header
	flags   byte
}

type IndexTicket struct {
	seen     bool
	uid      uint32
	location int
	flags    byte
}

var stat_mutex sync.Mutex

func (t *ResponseTicket) Stat(dir string) (err error) {
	first_byte := fmt.Sprintf("%02x", t.digest[0])
	rest_bytes := fmt.Sprintf("%02x", t.digest[1:])
	stat_mutex.Lock()
	defer stat_mutex.Unlock()
	if _, e := os.Stat(filepath.Join(dir, first_byte, rest_bytes)); e == nil {
		return nil
	} else if _, e := os.Stat(filepath.Join(dir, first_byte, rest_bytes) + ".gz"); e == nil {
		return nil
	} else {
		if e := os.MkdirAll(filepath.Join(dir, first_byte), os.ModePerm); e != nil {
			panic(e)
		} else if f, e := os.Create(filepath.Join(dir, first_byte, rest_bytes)); e != nil {
			panic(e)
		} else if _, e := t.WriteTo(f); e != nil {
			panic(e)
		} else if e := f.Close(); e != nil {
			panic(e)
		}
		return fmt.Errorf("did not exist")
	}
}

func (t *ResponseTicket) WriteTo(w io.Writer) (int64, error) {
	if n, e := WriteHeaders(t.headers, w); e != nil {
		return int64(n), e
	} else if _, e := w.Write([]byte{'\n'}); e != nil {
		panic(e)
	} else {
		return int64(n + 1), nil
	}
}

func (t *ResponseTicket) Read(b []byte) (n int, e error) {
	if len(b) < 5+digest_length {
		return 0, fmt.Errorf("not enough space")
	}
	for k := 0; k < 4; k++ {
		b[k] = byte(t.uid % 256)
		t.uid = t.uid / 256
	}
	copy(b[4:digest_length+4], t.digest[:])
	b[4+digest_length] = t.flags
	return 25, nil
}

type FlagTicket struct {
	old_flags byte
	new_flags byte
	digest    []byte
	custom    []string
}

func (fl *FlagTicket) WriteTo(w io.Writer) (n int64, e error) {
	first := fmt.Sprintf("%02x", fl.digest[0])
	rest := fmt.Sprintf("%02x", fl.digest[1:])
	if tags := fl.Tags(); len(tags) != 0 {
		if k, e := fmt.Fprintf(w, "%s %s\n", strings.Join(tags, " "), filepath.Join(*targetdir, first, rest)); e != nil {
			return n + int64(k), e
		} else {
			n += int64(k)
		}
	}
	return n, nil
}

func (fl *FlagTicket) Tags() []string {
	tags := make([]string, 0, 6)
	if len(fl.custom) != 0 {
		tags = append(tags, fl.custom...)
	}
	if (fl.old_flags)%0x02 != (fl.new_flags)%0x02 {
		switch (fl.new_flags) % 0x02 {
		case 0x00:
			tags = append(tags, "+unread")
		case 0x01:
			tags = append(tags, "-unread")
		}
	}
	for k, b := range []byte{0x02, 0x04, 0x08, 0x10} {
		if (fl.old_flags/b)%0x02 != (fl.new_flags/b)%0x02 {
			switch (fl.new_flags / b) % 0x02 {
			case 0x00:
				tags = append(tags, fmt.Sprintf("-%s", taglist[k+1]))
			case 0x01:
				tags = append(tags, fmt.Sprintf("+%s", taglist[k+1]))
			}
		}
	}
	return tags
}
