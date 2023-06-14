package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
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
var lastmodfile = flag.String("lastmod", "mail/.lastmod", "lastmod file")
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
			*lastmodfile = filepath.Join(s, *lastmodfile)
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
	var rev, arev string
	if r, a, e := Revision(*lastmodfile); e != nil {
		rev = ".."
		arev = ".."
	} else {
		rev = r
		arev = a
	}
	// same unread, deleted works for all folders
	// do not need to regenerate and sort each time
	// unread is flipped so need to check which are unread from ..lastmod
	unread, err := NotmuchTag("unread", arev)
	if err != nil {
		panic(err)
	}

	// check which are deleted from lastmod..
	deleted, err := NotmuchTag("deleted", rev)
	if err != nil {
		panic(err)
	}

	// check which are replied from lastmod..
	replied, err := NotmuchTag("replied", rev)
	if err != nil {
		panic(err)
	}

	// check which are replied from lastmod..
	forwarded, err := NotmuchTag("forwarded", rev)
	if err != nil {
		panic(err)
	}

	// at the very end, update notmuch tags
	// write valid paths which will contain messages to path_buffer
	// one on each line
	path_buffer := bytes.NewBuffer(nil)
	maybe_delete_buffer := bytes.NewBuffer(nil)

	// last in first out
	defer SaveLastModFile(*lastmodfile)
	defer UpdateNotmuch(path_buffer)
	defer MaybeDeleteHandler(*targetdir, *indexdir, maybe_delete_buffer, path_buffer)

	config_chan := make(chan io.ReadCloser, len(conf_paths))
	for _, cp := range conf_paths {
		if f, e := os.Open(cp); e != nil {
			fmt.Fprintf(os.Stderr, "ignoring: %s\n", cp)
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

					// read old index file
					indexbytes := func(indexfile string) (indexbytes [][digest_length + 5]byte) {
						var rb *bufio.Reader
						if f, e := os.Open(indexfile); e != nil {
							return nil
						} else if i, e := f.Stat(); e != nil {
							panic(e)
						} else {
							defer f.Close()
							rb = bufio.NewReaderSize(f, int(i.Size()))
							indexbytes = make([][digest_length + 5]byte, int(i.Size())/(digest_length+4+1))
						}
						for k := 0; k < len(indexbytes); k++ {
							rb.Read(indexbytes[k][:])
						}
						return indexbytes
					}(indexfile)

					// update remote \Seen flags
					unread_local_to_remote_update_chan := make(chan uint32)
					go LocalToRemoteFlag(client_chan, unread_local_to_remote_update_chan, "\\Seen", nil)
					Filter(unread, indexbytes, true, 0x01, unread_local_to_remote_update_chan)

					// update remote \Answered flags
					replied_local_to_remote_update_chan := make(chan uint32)
					go LocalToRemoteFlag(client_chan, replied_local_to_remote_update_chan, "\\Answered", nil)
					Filter(replied, indexbytes, false, 0x02, replied_local_to_remote_update_chan)

					// update remote \Deleted flags
					deleted_local_to_remote_update_chan := make(chan uint32)
					go LocalToRemoteFlag(client_chan, deleted_local_to_remote_update_chan, "\\Deleted", nil)
					Filter(deleted, indexbytes, false, 0x04, deleted_local_to_remote_update_chan)

					// update remote $Forwarded flags
					forwarded_local_to_remote_update_chan := make(chan uint32)
					go LocalToRemoteFlag(client_chan, forwarded_local_to_remote_update_chan, "$Forwarded", nil)
					Filter(forwarded, indexbytes, false, 0x08, forwarded_local_to_remote_update_chan)

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
							var tags string

							if (fl.flags)%0x02 == 0x01 {
								tags += "-unread "
							}
							if (fl.flags/0x02)%0x02 == 0x01 {
								tags += "+replied "
							}
							if (fl.flags/0x04)%0x02 == 0x01 {
								tags += "+deleted "
							}
							if (fl.flags/0x08)%0x02 == 0x01 {
								tags += "+forwarded "
							}

							if tags != "" {
								fmt.Fprintf(path_buffer, "%s%s", tags, filepath.Join(*targetdir, first, rest))
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
									case "\\Answered":
										new_flags += 0x02
									case "\\Deleted":
										new_flags += 0x04
									}
								}
								if old_flags != new_flags {
									LR_flag_update_chan <- &FlagTicket{
										flags:  new_flags,
										digest: indexbytes[k][4 : 4+digest_length],
									}
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
									case "\\Deleted":
										ticket.flags += 0x04
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

func MaybeDeleteHandler(targetdir string, indexdir string, maybe_delete_buffer *bytes.Buffer, path_buffer io.Writer) error {
	if maybe_delete_buffer.Len() == 0 {
		return nil
	}
	var tmp [digest_length]byte
	indexbytes := make([][digest_length]byte, 0, 2048)
	rb := new(bufio.Reader)
	filepath.WalkDir(indexdir, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() || len(d.Name()) != 10 {
			return nil
		}
		if f, e := os.Open(path); e != nil {
			return e
		} else if _, e := f.Stat(); e != nil {
			panic(e)
		} else {
			defer f.Close()
			rb.Reset(f)
		}
		for {
			rb.Discard(4)
			if n, e := rb.Read(tmp[:]); n != digest_length || e != nil {
				break
			} else {
				indexbytes = append(indexbytes, tmp)
			}
			rb.Discard(1)
		}
		return nil
	})
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
