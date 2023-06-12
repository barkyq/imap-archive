package main

import (
	"flag"

	"net/mail"
	"os"

	"crypto/sha256"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

var continue_flag = flag.Int("c", 0, "index from which to resume")

func main() {
	flag.Parse()
	addr, a, _, e := LoadConfig(os.Stdin)
	if e != nil {
		panic(e)
	}

	c, e := client.DialTLS(addr, nil)
	if e != nil {
		panic(e)
	}
	defer c.Logout()
	if e := c.Authenticate(a); e != nil {
		panic(e)
	}
	if stat, e := c.Select("INBOX", true); e != nil {
		panic(e)
	} else {
		section := &imap.BodySectionName{
			BodyPartName: imap.BodyPartName{
				Specifier: imap.HeaderSpecifier,
				Fields:    header_list,
			},
			Peek: true,
		}
		fetch_items := []imap.FetchItem{section.FetchItem()}
		fetch_seq := new(imap.SeqSet)
		if stat.Messages > 0 {
			fetch_seq.AddRange(1, stat.Messages)
		} else {
			return
		}
		fetch_chan := make(chan *imap.Message)
		donechan := make(chan struct{})
		hasher := sha256.New()
		go func() {
			for message := range fetch_chan {
				if m, e := mail.ReadMessage(message.GetBody(section)); e != nil {
					continue
				} else {
					WriteHeaders(m.Header, hasher)
					fmt.Printf("%x\n", hasher.Sum(nil))
					hasher.Reset()
				}
			}
			donechan <- struct{}{}
		}()
		if e := c.Fetch(fetch_seq, fetch_items, fetch_chan); e != nil {
			panic(e)
		} else {
			<-donechan
		}
	}
}
