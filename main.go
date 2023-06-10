package main

import (
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-maildir"
)

var continue_flag = flag.Int("c", 0, "index from which to resume")

func main() {
	flag.Parse()
	addr, a, directory, e := LoadConfig(os.Stdin)
	if e != nil {
		panic(e)
	}

	c, e := client.DialTLS(addr, nil)
	if e != nil {
		panic(e)
	}
	defer c.Logout()
	// if _, y, e := a.Start(); e == nil {
	// 	fmt.Println(base64.StdEncoding.EncodeToString(y))
	// }
	if e := c.Authenticate(a); e != nil {
		panic(e)
	}
	// if cap, e := c.Capability(); e != nil {
	// 	panic(e)
	// } else {
	// 	for k, v := range cap {
	// 		fmt.Println(k, v)
	// 	}
	// }
	ch := make(chan *imap.MailboxInfo)
	folder_chan := make(chan []string)
	go func() {
		folders := make([]string, 0, 128)
		for i := range ch {
			if e := os.MkdirAll(filepath.Join(directory, i.Name), os.ModePerm); e != nil {
				panic(e)
			}
			folders = append(folders, i.Name)
		}
		folder_chan <- folders
		close(folder_chan)
	}()
	if e := c.List("/", "*", ch); e != nil {
		panic(e)
	}

	donechan := make(chan struct{}, 1)
	donechan <- struct{}{}
	for folders := range folder_chan {
		for k, f := range folders {
			fmt.Println(k, f)
			if k < *continue_flag {
				continue
			}
			<-donechan
			if stat, e := c.Select(f, true); e != nil {
				panic(e)
			} else {
				section := &imap.BodySectionName{Peek: true}
				fetch_items := []imap.FetchItem{section.FetchItem(), imap.FetchUid}
				fetch_seq := new(imap.SeqSet)
				if stat.Messages > 0 {
					fetch_seq.AddRange(1, stat.Messages)
				} else {
					donechan <- struct{}{}
					continue
				}
				fetch_chan := make(chan *imap.Message)
				go func() {
					for msg := range fetch_chan {
						if message, e := mail.ReadMessage(msg.GetBody(section)); e != nil {
							continue
						} else {
							filename := filepath.Join(directory, f, fmt.Sprintf("msg_%d", msg.Uid))
							fi, e := os.Create(filename)
							if e != nil {
								panic(e)
							}

							for _, h := range []string{
								"From",
								"To",
								"Subject",
								"In-Reply-To",
								"References",
								"Date",
								"Message-ID",
								"MIME-Version",
								"Content-Type",
								"Content-Disposition",
								"Content-Transfer-Encoding",
							} {
								if v := message.Header.Get(h); v != "" {
									fmt.Fprintf(fi, "%s: %s\n", h, v)
								}
							}
							fmt.Fprintf(fi, "\n")
							if _, e := io.Copy(fi, message.Body); e != nil {
								panic(e)
							}
							if e := fi.Close(); e != nil {
								panic(e)
							}

						}
					}
					donechan <- struct{}{}
				}()
				if e := c.Fetch(fetch_seq, fetch_items, fetch_chan); e != nil {
					panic(e)
				}
			}
		}
		<-donechan
	}
}

// flag handler
func parseFlags(in_flags []string) (out_flags []maildir.Flag) {
	for _, f := range in_flags {
		switch f {
		case "\\Seen":
			out_flags = append(out_flags, maildir.FlagSeen)
		case "\\Answered":
			out_flags = append(out_flags, maildir.FlagReplied)
		}
	}
	return
}
