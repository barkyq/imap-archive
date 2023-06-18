package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/emersion/go-imap/client"
)

func (conf *Config) InitClient() (cc chan *client.Client, e error) {
	addr, a, e := LoadConfig(conf.r)
	if e != nil {
		return nil, e
	}
	conf.r.Close()
	conf.addr = addr
	conf.a = a
	conf.salt = make([]byte, digest_length)
	if _, ir, e := a.Start(); e != nil {
		panic(e)
	} else {
		// fmt.Println(addr)
		// enc := base64.NewEncoder(base64.StdEncoding, os.Stderr)
		// enc.Write(ir)
		// enc.Close()
		// fmt.Println()
		copy(conf.salt[:], ir)
	}
	client_chan := make(chan *client.Client, 1)
	if c, e := client.DialTLS(addr, nil); e != nil {
		return nil, e
	} else if e := c.Authenticate(a); e != nil {
		return nil, e
	} else {
		// for idling
		// defer c.Logout()
		client_chan <- c
	}
	return client_chan, nil
}

func InitHandler(cc chan *client.Client, conf *Config, unsorted_index_chan chan *IndexData) error {
	for k, mailbox := range conf.folders {
		if c := <-cc; c == nil {
			return fmt.Errorf("client is nil")
		} else if _, e := c.Select(mailbox, false); e != nil {
			return e
		} else {
			id := new(IndexData)
			id.filename = filepath.Join(*indexdir, GenerateMailboxID(mailbox, conf.addr, conf.salt))
			id.cc = cc
			id.k = k
			id.addr = conf.addr
			id.mailboxname = mailbox
			if e := id.ReadIndexFile(); e != nil && !os.IsNotExist(e) {
				return e
			} else {
				unsorted_index_chan <- id
				cc <- c
			}
		}
	}
	return nil
}

func MaybeDeleteHandler(targetdir string, full_index_buffer *bytes.Buffer, maybe_delete_buffer *bytes.Buffer, path_buffer io.Writer) error {
	if maybe_delete_buffer.Len() == 0 {
		return nil
	}
	var tmp [digest_length]byte
	indexbytes := make([][digest_length]byte, full_index_buffer.Len()/digest_length)
	for k := 0; k < len(indexbytes); k++ {
		if n, e := full_index_buffer.Read(indexbytes[k][:]); e != nil || n != digest_length {
			panic(e)
		}
	}
	sort.Slice(indexbytes, func(i, j int) bool {
		for k, b := range indexbytes[i] {
			switch {
			case b < indexbytes[j][k]:
				return true
			case b == indexbytes[j][k]:
				continue
			case b > indexbytes[j][k]:
				return false
			}
		}
		return true
	})

	var terminal bool
	var first_byte, rest_bytes, path string
	for {
		terminal = false
		if n, e := maybe_delete_buffer.Read(tmp[:]); e != nil || n != digest_length {
			break
		}
		sort.Search(len(indexbytes), func(i int) bool {
			for k, b := range indexbytes[i] {
				switch {
				case tmp[k] > b:
					return false
				case tmp[k] == b:
					continue
				case tmp[k] < b:
					return true
				}
			}
			terminal = true
			return true
		})
		if !terminal {
			// not found
			// truly deleted
			first_byte = fmt.Sprintf("%02x", tmp[0])
			rest_bytes = fmt.Sprintf("%02x", tmp[1:])
			path = filepath.Join(targetdir, first_byte, rest_bytes)
			fmt.Fprintf(path_buffer, "+deleted %s\n", path)
		}
	}
	return nil
}
