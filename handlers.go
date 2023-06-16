package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap"
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

func FetchHandler(cc chan *client.Client, conf *Config, logger io.Writer, unsorted_index_chan chan *IndexData, path_buffer *bytes.Buffer, maybe_delete_buffer *bytes.Buffer) {
	uid_seq := new(imap.SeqSet)
	for k, mailbox := range conf.folders {
		func(k int, mailbox string) {
			mailboxid := GenerateMailboxID(mailbox, conf.addr, conf.salt)
			indexfile := filepath.Join(*indexdir, mailboxid)

			if c := <-cc; c == nil {
				panic("client is nil")
			} else if stat, e := c.Select(mailbox, false); e != nil {
				// TODO: attempt reconnect
				fmt.Fprintf(logger, "%s: %s\n", mailboxid, e.Error())
				return
			} else if stat.Messages > 0 {
				uid_seq.AddRange(1, stat.Messages)
				cc <- c
			} else {
				fmt.Fprintf(logger, "%s: no messages on remote... exiting\n", mailboxid)
				if e := os.Truncate(indexfile, 0); e != nil && !os.IsNotExist(e) {
					panic(e)
				}
				return
			}
			// TODO: try to get rid of this waitgroup.
			var wg sync.WaitGroup
			defer wg.Wait()
			defer uid_seq.Clear()

			// read old index file
			var indexbytes [][digest_length + 5]byte
			if id, e := ReadIndexFile(mailbox, indexfile); e != nil {
				indexbytes = nil
			} else {
				indexbytes = id.indexbytes
			}
			defer func() {
				// read new index file
				if id, e := ReadIndexFile(mailbox, indexfile); e != nil {
					panic(e)
				} else {
					id.cc = cc
					unsorted_index_chan <- id
				}
			}()

			// TODO: refactor? these channels will only have one write/read
			fetch_seq_chan := make(chan *imap.SeqSet)
			full_fetch_seq_chan := make(chan *imap.SeqSet)

			uid_chan := make(chan *imap.Message)
			fetch_chan := make(chan *imap.Message)
			full_fetch_chan := make(chan *imap.Message)

			// remote -> local flag updating
			LR_flag_update_chan := make(chan *FlagTicket)

			// TODO clean up flag handling
			go func() {
				for fl := range LR_flag_update_chan {
					first := fmt.Sprintf("%02x", fl.digest[0])
					rest := fmt.Sprintf("%02x", fl.digest[1:])

					if tags := fl.Tags(); len(tags) != 0 {
						fmt.Fprintf(path_buffer, "%s %s", strings.Join(tags, " "), filepath.Join(*targetdir, first, rest))
						fmt.Fprintf(os.Stderr, "%s %s\n", strings.Join(tags, " "), filepath.Join(*targetdir, first, rest))
						path_buffer.Write([]byte{'\n'})
					}
				}
			}()

			// responses from FETCH headers
			response_chan := make(chan *ResponseTicket)

			// index file copying
			// need to initialize `rp` reader before writes to `wp` (blocking)
			rp, wp := io.Pipe()

			wg.Add(1)
			go func() {
				defer wg.Done()
				full_fetch_seq := new(imap.SeqSet)
				tmp_index, e := os.CreateTemp(".", ".tmp_index_")
				if e != nil {
					panic(e)
				}
				wb := bufio.NewWriter(tmp_index)
				var indexline [digest_length + 4]byte

				// rp reader
				for {
					// keys which are still on server; flags already updated
					if n, e := rp.Read(indexline[:]); e == io.EOF {
						break
					} else if e != nil || (n != digest_length+4 && n != 1) {
						panic(fmt.Sprintf("%s:%d", e, n))
					} else {
						wb.Write(indexline[:n])
					}
				}
				for ticket := range response_chan {
					if _, e := ticket.Stat(*targetdir); e != nil {
						// stat failed, file does not exist
						full_fetch_seq.AddNum(ticket.uid)
					}
					if _, e := ticket.WriteTo(wb); e != nil {
						panic(e)
					}
					var custom string
					switch k {
					case 0:
						custom = "inbox"
					case 1:
						custom = "sent"
					}
					LR_flag_update_chan <- &FlagTicket{
						old_flags: 0x00,
						new_flags: ticket.flags,
						digest:    ticket.digest[:],
						custom:    custom,
					}
				}
				close(LR_flag_update_chan)
				if e := wb.Flush(); e != nil {
					panic(e)
				} else if e := tmp_index.Close(); e != nil {
					panic(e)
				} else if e := os.Rename(tmp_index.Name(), indexfile); e != nil {
					panic(e)
				} else {
					full_fetch_seq_chan <- full_fetch_seq
				}
			}()

			go func() {
				fetch_seq := new(imap.SeqSet)
				uids := make([]int, len(indexbytes))
				survived := make([]bool, len(indexbytes))
				// uids should be sorted
				for k := 0; k < len(indexbytes); k++ {
					uids[k] = int(indexbytes[k][0]) + int(indexbytes[k][1])*256 + int(indexbytes[k][2])*65536 + int(indexbytes[k][3])*16777216
				}
				for message := range uid_chan {
					// fmt.Println(message)
					if k := sort.SearchInts(uids, int(message.Uid)); k == len(uids) {
						// not found in index file
						fetch_seq.AddNum(message.Uid)
					} else {
						// was found in index file
						// rewrite to new index file, with updated flags
						survived[k] = true
						old_flags := indexbytes[k][4+digest_length]
						var new_flags byte
						for _, fl := range message.Flags {
							switch fl {
							case "\\Seen":
								new_flags += 0x01
							case flaglist[0]:
								new_flags += 0x02
							case flaglist[1]:
								new_flags += 0x02 << 1
							case flaglist[2]:
								new_flags += 0x02 << 2
							case flaglist[3]:
								new_flags += 0x02 << 3
							}
						}
						LR_flag_update_chan <- &FlagTicket{
							old_flags: old_flags,
							new_flags: new_flags,
							digest:    indexbytes[k][4 : 4+digest_length],
						}

						// wp writer
						// do not rewrite indexbytes to avoid race condition
						wp.Write(indexbytes[k][:4+digest_length])
						wp.Write([]byte{new_flags})
					}
				}
				for k, v := range survived {
					if v == true {
						continue
					} else {
						maybe_delete_buffer.Write(indexbytes[k][4 : 4+digest_length])
					}
				}
				wp.Close()
				fetch_seq_chan <- fetch_seq
			}()

			if c := <-cc; c == nil {
				panic("client is nil")
			} else if e := c.Fetch(uid_seq, uid_fetch_items, uid_chan); e != nil {
				panic(e)
			} else {
				cc <- c
			}
			// end uid stage

			// start fetch header stage
			fetch_seq := <-fetch_seq_chan
			close(fetch_seq_chan)

			if fetch_seq.Empty() {
				fmt.Fprintf(logger, "%s: no headers to fetch\n", mailboxid)
				close(response_chan)
				<-full_fetch_seq_chan
				wg.Wait()
				return
			} else {
				fmt.Fprintf(logger, "%s: fetching headers: %s\n", mailboxid, fetch_seq.String())
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				hasher := sha256.New()
				counter := 0
				for message := range fetch_chan {
					if m, e := mail.ReadMessage(message.GetBody(canonical_header_section)); e != nil {
						continue
					} else {
						if n, e := WriteHeaders(m.Header, hasher); e != nil {
							panic(e)
						} else {
							counter += n
						}
						ticket := new(ResponseTicket)
						for _, fl := range message.Flags {
							switch fl {
							case "\\Seen":
								ticket.flags += 0x01
							case flaglist[0]:
								ticket.flags += 0x02
							case flaglist[1]:
								ticket.flags += 0x02 << 1
							case flaglist[2]:
								ticket.flags += 0x02 << 2
							case flaglist[3]:
								ticket.flags += 0x02 << 3
							}
						}
						ticket.uid = message.Uid
						copy(ticket.digest[:], hasher.Sum(nil))
						hasher.Reset()
						response_chan <- ticket
					}
				}
				close(response_chan)
				fmt.Fprintf(logger, "%s: total header bytes hashed: %d\n", mailboxid, counter)
			}()

			if c := <-cc; c == nil {
				panic("client is nil")
			} else if e := c.UidFetch(fetch_seq, canonical_header_fetch_items, fetch_chan); e != nil {
				panic(e)
			} else {
				cc <- c
			}
			// end fetch headers stage

			// start full fetch stage
			full_fetch_seq := <-full_fetch_seq_chan
			close(full_fetch_seq_chan)

			if full_fetch_seq.Empty() {
				fmt.Fprintf(logger, "%s: no messages to fetch\n", mailboxid)
				wg.Wait()
				return
			} else {
				fmt.Fprintf(logger, "%s: fetching messages: %s\n", mailboxid, full_fetch_seq.String())
			}

			archive_tickets_1, archive_tickets_2 := make(chan *ArchiveTicket), make(chan *ArchiveTicket)

			// generate archive tickets from server response
			wg.Add(1)
			go func() {
				defer wg.Done()
				batons := make(chan *ArchiveTicket, num_batons)

				for i := 0; i < num_batons; i++ {
					a := &ArchiveTicket{
						hasher:  sha256.New(),
						batons:  batons,
						tickets: archive_tickets_1,
						file:    nil,
						msg:     new(mail.Message),
						rb:      new(bufio.Reader),
						wb:      new(bufio.Writer),
					}
					batons <- a
				}

				for message := range full_fetch_chan {
					t := <-batons
					if m, e := mail.ReadMessage(message.GetBody(full_section)); e != nil {
						panic(e)
					} else {
						t.rb.Reset(m.Body)
						t.msg = m
						t.Submit()
					}
				}
				for i := 0; i < num_batons; i++ {
					<-batons
				}
				close(archive_tickets_1)
			}()

			// handle archive tickets before hash
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ticket := range archive_tickets_1 {
					if e := ticket.Hash(); e != nil {
						panic(e)
					} else {
						archive_tickets_2 <- ticket
					}
				}
				close(archive_tickets_2)
			}()

			// handle archive tickets after hash
			wg.Add(1)
			go func() {
				defer wg.Done()
				if n, e := HandleArchiveTickets(*targetdir, archive_tickets_2); e != nil {
					panic(e)
				} else {
					fmt.Fprintf(logger, "%s: megabytes written to archive: %0.6f\n", mailboxid, float64(n)/1000000)
				}
			}()

			// full fetch
			if c := <-cc; c == nil {
				panic("client is nil")
			} else if e := c.UidFetch(full_fetch_seq, full_fetch_items, full_fetch_chan); e != nil {
				panic(e)
			} else {
				cc <- c
			}
		}(k, mailbox)
	}
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
