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
		if _, e := os.Stat(filepath.Join(targetdir, first_byte, rest_bytes)); e == nil {
			// already exists
			ticket.Release()
			continue
		} else {
			go func(ticket *ArchiveTicket, first_byte string, rest_bytes string) {
				defer ticket.Release()
				if g, e := os.Create(filepath.Join(targetdir, first_byte, rest_bytes)); e != nil {
					panic(e)
				} else {
					ticket.wb.Reset(g)
					if n, e := WriteMessage(ticket.msg.Header, ticket.rb, ticket.wb); e != nil {
						panic(e)
					} else if e := ticket.wb.Flush(); e != nil {
						panic(e)
					} else {
						g.Close()
						sizes <- n
					}
				}
			}(ticket, first_byte, rest_bytes)
		}
	}
	close(sizes)

	return <-counterchan, nil
}

type ResponseTicket struct {
	mailbox string
	uid     uint32
	digest  [digest_length]byte
	flags   byte
}

func (t *ResponseTicket) Stat(dir string) (fs.FileInfo, error) {
	first_byte := fmt.Sprintf("%02x", t.digest[0])
	rest_bytes := fmt.Sprintf("%02x", t.digest[1:])
	return os.Stat(filepath.Join(dir, first_byte, rest_bytes))
}

// 4 byte encoding of uid
func (t *ResponseTicket) WriteTo(w io.Writer) (n int64, e error) {
	var uidbytes [4]byte
	for k := range uidbytes {
		uidbytes[k] = byte(t.uid % 256)
		t.uid = t.uid / 256
	}
	if _, e := w.Write(uidbytes[:]); e != nil {
		return 4, e
	} else if _, e := w.Write(t.digest[:]); e != nil {
		return 4 + digest_length, e
	} else if _, e := w.Write([]byte{t.flags}); e != nil {
		return 5 + digest_length, e
	} else {
		return 5 + digest_length, nil
	}
}

type FlagTicket struct {
	flags  byte
	digest []byte
}
