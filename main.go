package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const digest_length = 20
const num_batons = 4

var indexdir = flag.String("index", "mail/.index", "index file directory")
var targetdir = flag.String("t", "mail/target", "target directory")
var portable = flag.Bool("p", false, "portable (not relative HOME)")

func main() {
	flag.Parse()
	// initialize the index directory
	if !*portable {
		if s, e := os.UserHomeDir(); e != nil {
			panic(e)
		} else {
			*targetdir = filepath.Join(s, *targetdir)
			*indexdir = filepath.Join(s, *indexdir)
		}
	}
	if e := os.MkdirAll(*indexdir, os.ModePerm); e != nil {
		panic(e)
	}

	conf_paths := func() []string {
		cp := make([]string, 0, 3)
		stdin := bufio.NewReader(os.Stdin)
		for {
			if l, p, e := stdin.ReadLine(); e != nil {
				break
			} else if p {
				panic("isPrefix")
			} else {
				cp = append(cp, fmt.Sprintf("%s", l))
			}
		}
		return cp
	}()

	// same unread works for all folders
	// do not need to regenerate and sort each time
	unread, err := NotmuchUnread()
	if err != nil {
		panic(err)
	}

	// at the very end, update notmuch tags
	// write valid paths which will contain messages to path_buffer
	// one on each line
	path_buffer := bytes.NewBuffer(nil)
	defer UpdateNotmuch(path_buffer)

	config_chan := make(chan io.ReadCloser, len(conf_paths))
	for _, cp := range conf_paths {
		if f, e := os.Open(cp); e != nil {
			fmt.Fprintf(os.Stderr, "invalid config path: %s\n", cp)
			continue
		} else if !strings.HasSuffix(f.Name(), ".gpg") {
			config_chan <- f
		} else {
			// has gpg suffix
			cmd := exec.Command("/usr/bin/gpg", "-qd", "-")
			cmd.Stdin = f
			rp, wp := io.Pipe()
			cmd.Stdout = wp

			go func() {
				if e := cmd.Run(); e != nil {
					panic(e)
				}
			}()
			config_chan <- rp
		}
	}
	close(config_chan)

	var mwg sync.WaitGroup
	for conf := range config_chan {
		mwg.Add(1)
		go func(conf io.ReadCloser) {
			defer mwg.Done()
			addr, a, folder_list, e := LoadConfig(conf)
			if e != nil {
				panic(e)
			}
			conf.Close()
			var salt [digest_length]byte
			if _, ir, e := a.Start(); e != nil {
				panic(e)
			} else {
				copy(salt[:], ir)
			}
			client_chan := make(chan *client.Client, 1)
			if c, e := client.DialTLS(addr, nil); e != nil {
				panic(e)
			} else if e := c.Authenticate(a); e != nil {
				panic(e)
			} else {
				defer c.Logout()
				client_chan <- c
			}
			uid_seq := new(imap.SeqSet)
			for _, mailbox := range folder_list {
				func(mailbox string) {
					mailboxid := GenerateMailboxID(mailbox, addr, salt)
					indexfile := filepath.Join(*indexdir, mailboxid)

					// reset
					uid_seq.Clear()

					if c := <-client_chan; c == nil {
						panic("client is nil")
					} else if stat, e := c.Select(mailbox, false); e != nil {
						// TODO: attempt reconnect
						fmt.Fprintf(os.Stderr, "%s: %s\n", mailboxid, e.Error())
						return
					} else if stat.Messages > 0 {
						uid_seq.AddRange(1, stat.Messages)
						client_chan <- c
					} else {
						fmt.Fprintf(os.Stderr, "%s: no messages on remote... exiting\n", mailboxid)
						if e := os.Truncate(indexfile, 0); e != nil && !os.IsNotExist(e) {
							panic(e)
						}
						return
					}
					// TODO: try to get rid of this waitgroup.
					var wg sync.WaitGroup

					// first things first... read old index file
					// send results on indexbyte_chan
					indexbyte_chan := make(chan [][digest_length + 5]byte, 1)

					go func() {
						var indexbytes [][digest_length + 5]byte
						var rb *bufio.Reader
						if f, e := os.Open(indexfile); e != nil {
							indexbyte_chan <- nil
							return
						} else if i, e := f.Stat(); e != nil {
							panic(e)
						} else {
							defer f.Close()
							rb = bufio.NewReaderSize(f, int(i.Size()))
							indexbytes = make([][25]byte, int(i.Size())/(digest_length+4+1))
						}
						for k := 0; k < len(indexbytes); k++ {
							rb.Read(indexbytes[k][:])
						}
						indexbyte_chan <- indexbytes
					}()

					// local -> remote flag update channel
					RL_flag_update_chan := make(chan uint32)

					// update remote flags
					go func() {
						c := <-client_chan
						uids_seen := new(imap.SeqSet)
						for x := range RL_flag_update_chan {
							uids_seen.AddNum(x)
						}
						if uids_seen.Empty() {
							client_chan <- c
							return
						}
						ch := make(chan *imap.Message)
						go func() {
							ids_seen := new(imap.SeqSet)
							for m := range ch {
								ids_seen.AddNum(m.SeqNum)
							}
							flags := []interface{}{imap.SeenFlag}
							if e := c.Store(ids_seen, imap.FormatFlagsOp(imap.SetFlags, false), flags, nil); e != nil {
								panic(e)
							} else {
								client_chan <- c
							}
						}()
						if e := c.UidFetch(uids_seen, []imap.FetchItem{imap.FetchUid}, ch); e != nil {
							panic(e)
						}
					}()

					// blocking
					// updating flags
					func() {
						indexbytes := <-indexbyte_chan
						defer func() {
							close(RL_flag_update_chan)
							indexbyte_chan <- indexbytes
						}()

						if len(unread) == 0 {
							// everything is read, so put \Seen flag to everything
							for _, ibs := range indexbytes {
								uid := uint32(ibs[0]) + uint32(ibs[1])*256 + uint32(ibs[2])*65536 + uint32(ibs[3])*16777216
								RL_flag_update_chan <- uid
							}
							return
						}

						uch, ich := make(chan []byte, num_batons), make(chan []byte, num_batons)
						for i := 0; i < num_batons; i++ {
							uch <- make([]byte, 4)
							ich <- make([]byte, digest_length-1)
						}
						for _, ibs := range indexbytes {
							if ibs[digest_length+4]%2 == 0x01 {
								// already has SEEN flag
								continue
							}
							u := <-uch
							copy(u, ibs[:4])
							i := <-ich
							copy(i, ibs[5:digest_length+4])
							go func(uids []byte, ibs []byte) {
								var terminal bool
								defer func() {
									uch <- uids
									ich <- ibs
								}()
								if sort.Search(len(unread), func(i int) bool {
									for k, b := range unread[i] {
										switch {
										case ibs[k] > b:
											return false
										case ibs[k] == b:
											continue
										case ibs[k] < b:
											return true
										}
									}
									terminal = true
									return true
								}); !terminal {
									// not found, means it is unread -> read
									uid := uint32(uids[0]) + uint32(uids[1])*256 + uint32(uids[2])*65536 + uint32(uids[3])*16777216
									RL_flag_update_chan <- uid
								}
							}(u, i)
						}
						for i := 0; i < num_batons; i++ {
							<-uch
							<-ich
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

					go func() {
						for fl := range LR_flag_update_chan {
							first := fmt.Sprintf("%02x", fl.digest[0])
							rest := fmt.Sprintf("%02x", fl.digest[1:])
							if fl.flags%2 == 1 {
								fmt.Fprintf(path_buffer, filepath.Join(*targetdir, first, rest))
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
						var indexbytes [digest_length + 5]byte

						// rp reader
						for {
							// keys which are still on server; flags already updated
							if n, e := rp.Read(indexbytes[:]); e == io.EOF {
								break
							} else if e != nil || n != digest_length+5 {
								panic(e)
							} else {
								wb.Write(indexbytes[:])
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
							if ticket.flags != 0x00 {
								LR_flag_update_chan <- &FlagTicket{
									flags:  ticket.flags,
									digest: ticket.digest[:],
								}
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
						indexbytes := <-indexbyte_chan
						uids := make([]int, len(indexbytes))
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
								old_flags := indexbytes[k][4+digest_length]
								var new_flags byte
								for _, fl := range message.Flags {
									switch fl {
									case "\\Seen":
										new_flags += 0x01
									case "\\Answered":
										new_flags += 0x02
									}
								}
								if old_flags != new_flags {
									LR_flag_update_chan <- &FlagTicket{
										flags:  new_flags,
										digest: indexbytes[k][4 : 4+digest_length],
									}
								}
								indexbytes[k][4+digest_length] = new_flags

								// wp writer
								wp.Write(indexbytes[k][:])
							}
						}
						wp.Close()
						fetch_seq_chan <- fetch_seq
					}()

					if c := <-client_chan; c == nil {
						panic("client is nil")
					} else if e := c.Fetch(uid_seq, uid_fetch_items, uid_chan); e != nil {
						panic(e)
					} else {
						client_chan <- c
					}
					// end uid stage

					// start fetch header stage
					fetch_seq := <-fetch_seq_chan
					close(fetch_seq_chan)

					if fetch_seq.Empty() {
						fmt.Fprintf(os.Stderr, "%s: no headers to fetch\n", mailboxid)
						close(response_chan)
						<-full_fetch_seq_chan
						wg.Wait()
						return
					} else {
						fmt.Fprintf(os.Stderr, "%s: fetching headers: %s\n", mailboxid, fetch_seq.String())
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
								ticket.mailbox = mailbox
								for _, fl := range message.Flags {
									switch fl {
									case "\\Seen":
										ticket.flags += 0x01
									case "\\Answered":
										ticket.flags += 0x02
									}
								}
								ticket.uid = message.Uid
								copy(ticket.digest[:], hasher.Sum(nil))
								hasher.Reset()
								response_chan <- ticket
							}
						}
						close(response_chan)
						fmt.Fprintf(os.Stderr, "%s: total header bytes hashed: %d\n", mailboxid, counter)
					}()

					if c := <-client_chan; c == nil {
						panic("client is nul")
					} else if e := c.UidFetch(fetch_seq, canonical_header_fetch_items, fetch_chan); e != nil {
						panic(e)
					} else {
						client_chan <- c
					}
					// end fetch headers stage

					// start full fetch stage
					full_fetch_seq := <-full_fetch_seq_chan
					close(full_fetch_seq_chan)

					if full_fetch_seq.Empty() {
						fmt.Fprintf(os.Stderr, "%s: no messages to fetch\n", mailboxid)
						wg.Wait()
						return
					} else {
						fmt.Fprintf(os.Stderr, "%s: fetching messages: %s\n", mailboxid, full_fetch_seq.String())
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
							fmt.Fprintf(os.Stderr, "%s: megabytes written to archive: %0.6f\n", mailboxid, float64(n)/1000000)
						}
					}()

					// full fetch
					if c := <-client_chan; c == nil {
						panic("client is nil")
					} else if e := c.UidFetch(full_fetch_seq, full_fetch_items, full_fetch_chan); e != nil {
						panic(e)
					} else {
						client_chan <- c
					}

					wg.Wait()
				}(mailbox)
			}
		}(conf)
	}
	mwg.Wait()
}
