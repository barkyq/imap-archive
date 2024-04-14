package main

import (
	"encoding/base64"
	"fmt"

	"os"
	"path/filepath"

	"github.com/emersion/go-imap/client"
)

func (conf *Config) InitClient() (cc chan *client.Client, e error) {
	salt, addr, a, e := LoadConfig(conf.r)
	if e != nil {
		return nil, e
	}
	conf.r.Close()
	conf.addr = addr
	conf.a = a
	if tmp, e := base64.URLEncoding.DecodeString(salt); e != nil {
		return nil, e
	} else {
		conf.salt = tmp
	}
	if !*printauth {
		// do nothing
	} else if m, ir, e := a.Start(); e != nil {
		panic(e)
	} else {
		fmt.Fprintln(os.Stdout, conf.PrintAuth(m, ir))
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
