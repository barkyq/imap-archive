package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/emersion/go-imap/client"
)

func LRFlagIdle(ctx context.Context, lastmodfile string, notmuchdir string, sic chan *IndexData, size int) error {
	ids := make([]*IndexData, 0, size)
	stopchans := make(map[string]chan struct{})
	idling := make(map[string]*bool)
	idling_mutex := make(map[string]*sync.Mutex)
	maybe_delete_buffer := bytes.NewBuffer(nil)
	for id := range sic {
		if _, ok := stopchans[id.addr]; !ok {
			stopchans[id.addr] = make(chan struct{})
		}
		id.stopidle = stopchans[id.addr]
		if _, ok := idling[id.addr]; !ok {
			b := false
			idling[id.addr] = &b
		}
		id.idling = idling[id.addr]
		if _, ok := idling_mutex[id.addr]; !ok {
			var wg sync.Mutex
			idling_mutex[id.addr] = &wg
		}
		id.idling_mutex = idling_mutex[id.addr]
		id.addbuffer = make([][5]byte, 0, 16)
		id.updates = make(chan client.Update, 32)
		id.indexbytes_mutex = new(sync.Mutex)
		// force initial update
		id.updates <- new(client.MailboxUpdate)
		go id.ListenForUpdates(maybe_delete_buffer)
		if id.k == 0 {
			go func(id *IndexData) {
				for {
					<-time.After(5 * time.Second)
					if e := id.Idle(); e != nil {
						panic(e)
					}
				}
			}(id)
		}
		ids = append(ids, id)
	}

	var uuid string
	var lastmod int
	if f, e := os.Open(lastmodfile); e != nil {
		return e
	} else if uuid, lastmod, e = readLastModFile(f); e != nil {
		return e
	} else if e = f.Close(); e != nil {
		return e
	}
	if ln, e := Watch(notmuchdir); e != nil {
		return e
	} else {
		for {
			if ctx.Err() != nil {
				wb := new(bufio.Writer)
				for _, id := range ids {
					// don't unlock
					id.indexbytes_mutex.Lock()
					if e := id.SaveIndexFile(wb); e != nil {
						panic(e)
					} else {
						if e := id.Disconnect(); e != nil {
							panic(e)
						}
					}
				}
				if e := SaveLastModFile(lastmodfile); e != nil {
					panic(e)
				}
				fmt.Fprintln(os.Stderr, "ending idle loop")
				return nil
			}
			<-time.After(3 * time.Second)
			// drain attempt
			for {
				select {
				case <-ln:
					continue
				default:
				}
				break
			}
			buffer, e := GetNotmuchTags(taglist, nil, uuid, lastmod)
			if e != nil {
				return e
			} else if buffer != nil {
				for _, id := range ids {
					id.indexbytes_mutex.Lock()
					seen_tags := id.GenerateSignal(0x01)
					for k, buf := range buffer {
						if k == 0 {
							for _, query := range buf {
								if loc, _, e := id.Search(5, query[:]); errors.Is(e, NotFound) {
									continue
								} else if e != nil {
									return e
								} else {
									seen_tags[loc] += 0x80
								}
							}
							for k, b := range seen_tags {
								switch b {
								case 0x01: // not unread + seen
								case 0x80: // unread + not seen
								case 0x81: // unread + seen
								case 0x00: // not unread + not seen
									var tmp [5]byte
									tmp[0] = 0x01
									id.indexbytes[k][digest_length+4] += 0x01
									copy(tmp[1:], id.indexbytes[k][:4])
									id.addbuffer = append(id.addbuffer, tmp)
								}
							}
						} else {
							for _, query := range buf {
								if _, result, e := id.Search(5, query[:]); errors.Is(e, NotFound) {
									continue
								} else if e != nil {
									return e
								} else if (result[digest_length+4]/(0x01<<k))%0x02 == 0x01 {
									continue
								} else {
									var tmp [5]byte
									tmp[0] = 0x01 << k
									result[digest_length+4] += 0x01 << k
									copy(tmp[1:], result[:4])
									id.addbuffer = append(id.addbuffer, tmp)
								}
							}
						}
					}
					if len(id.addbuffer) != 0 {
						if e := id.SubmitFlags(id.addbuffer, "+FLAGS"); e != nil {
							return e
						} else {
							id.addbuffer = id.addbuffer[:0]
						}
					}
					id.indexbytes_mutex.Unlock()
				}
			}
			if e := Reindex(uuid, lastmod); e != nil {
				fmt.Fprintln(os.Stderr, "reindex error... try again later")
			} else if u, l, e := LastMod(); e != nil {
				panic(e)
			} else {
				uuid = u
				lastmod = l
			}
			for {
				select {
				case <-ln:
					if _, l, e := LastMod(); e != nil {
						panic(e)
					} else if lastmod == l {
						continue
					}
				case <-ctx.Done():
				}
				break
			}
		}
	}
}
