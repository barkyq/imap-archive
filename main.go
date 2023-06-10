package main

import (
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"

	"bufio"
	"crypto/sha256"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

var continue_flag = flag.Int("c", 0, "index from which to resume")

func main() {
	flag.Parse()
	addr, a, directory, e := LoadConfig(os.Stdin)
	if e != nil {
		panic(e)
	}
	targetdir := filepath.Join(directory, "offline")
	c, e := client.DialTLS(addr, nil)
	if e != nil {
		panic(e)
	}
	defer c.Logout()
	if e := c.Authenticate(a); e != nil {
		panic(e)
	}
	ch := make(chan *imap.MailboxInfo)
	folder_chan := make(chan []string)
	go func() {
		folders := make([]string, 0, 128)
		for i := range ch {
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
		for k, folder := range folders {
			fmt.Println(k, folder)
			if k < *continue_flag {
				continue
			}
			<-donechan
			if stat, e := c.Select(folder, true); e != nil {
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
					hash := sha256.New()
					var first_byte string
					var rest_bytes string
					var digest [32]byte
					var header mail.Header
					var msg *mail.Message
					num_batons := 5
					batons := make(chan *bufio.Reader, num_batons)
					for i := 0; i < num_batons; i++ {
						batons <- new(bufio.Reader)
					}
					for message := range fetch_chan {
						if m, e := mail.ReadMessage(message.GetBody(section)); e != nil {
							continue
						} else {
							msg = m
							header = m.Header
							header["Folder"] = []string{folder}
							if _, e := WriteHeaders(header, hash); e != nil {
								panic(e)
							} else {
								copy(digest[:], hash.Sum(nil))
								hash.Reset()
							}
						}
						first_byte = fmt.Sprintf("%02x", digest[0])
						rest_bytes = fmt.Sprintf("%02x", digest[1:])
						if i, e := os.Stat(filepath.Join(targetdir, first_byte)); e != nil {
							if e := os.MkdirAll(filepath.Join(targetdir, first_byte), os.ModePerm); e != nil {
								panic(e)
							}
						} else if !i.IsDir() {
							panic(e)
						}
						if _, e := os.Stat(filepath.Join(targetdir, first_byte, rest_bytes)); e == nil {
							continue
						}
						rb := <-batons
						rb.Reset(msg.Body)
						if g, e := os.Create(filepath.Join(targetdir, first_byte, rest_bytes)); e != nil {
							panic(e)
						} else {
							go func(tf *os.File, first string, rest string, rb *bufio.Reader) {
								if _, e := WriteMessage(header, rb, tf); e != nil {
									panic(e)
								} else {
									tf.Close()
									batons <- rb
								}
							}(g, first_byte, rest_bytes, rb)
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
	for _, h := range header_list {
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
	"Folder",
}
