package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"sort"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type IndexData struct {
	mailboxname string
	filename    string
	k           int
	addr        string
	indexbytes  [][digest_length + 5]byte
	addbuffer   [][5]byte // first byte is flag, rest bytes are uint32
	cc          chan *client.Client
}

func read_uint32(x []byte) uint32 {
	return uint32(x[0]) + uint32(x[1])*256 + uint32(x[2])*65536 + uint32(x[3])*16777216
}

func (id *IndexData) GenerateIndexTickets() []*IndexTicket {
	indextickets := make([]*IndexTicket, len(id.indexbytes))
	for k, b := range id.indexbytes {
		indextickets[k] = &IndexTicket{
			uid:      read_uint32(b[:4]),
			location: k,
			flags:    b[digest_length+4],
		}
	}
	sort.Slice(indextickets, func(i, j int) bool {
		return indextickets[i].uid < indextickets[j].uid
	})
	return indextickets
}

func (id *IndexData) ForceUpdate(path_buffer *bytes.Buffer) {
	hasher := sha256.New()
	fmt.Fprintf(os.Stderr, "c: %s\n", filepath.Base(id.filename))

	if fetch, e := id.CompareUIDs(path_buffer); e != nil {
		panic(e)
	} else if fetch != nil {
		if full, e := id.FilterCanonicalHeaders(fetch, hasher, path_buffer); e != nil {
			panic(e)
		} else if full != nil {
			if e := id.HandleFullFetched(full); e != nil {
				panic(e)
			}
		}
	}
}

func (id *IndexData) CompareUIDs(path_buffer *bytes.Buffer) (chan *imap.Message, error) {
	c := <-id.cc
	if stat, e := c.Select(id.mailboxname, false); e != nil {
		select {
		case <-c.LoggedOut():
			return nil, nil
		default:
			panic(e)
		}
	} else if stat.Messages == 0 {
		id.indexbytes = nil
		id.cc <- c
		return nil, nil
	}

	uid_seq := new(imap.SeqSet)
	if stat := c.Mailbox(); stat.Name != id.mailboxname {
		return nil, fmt.Errorf("wrong mailbox selected")
	} else {
		uid_seq.AddRange(1, stat.Messages)
	}
	uid_chan := make(chan *imap.Message)
	fetch := make(chan *imap.Message)

	go func() {
		defer func() {
			id.cc <- c
		}()
		fetch_seq := new(imap.SeqSet)
		deleted := 0
		indextickets := id.GenerateIndexTickets()
		for msg := range uid_chan {
			if k := sort.Search(len(indextickets), func(i int) bool {
				return msg.Uid <= uint32(indextickets[i].uid)
			}); k == len(indextickets) {
				// not in index, need to fetch headers
				fetch_seq.AddNum(msg.Uid)
			} else {
				indextickets[k].seen = true
				var new_flags byte
				for _, fl := range msg.Flags {
					switch fl {
					case flaglist[0]:
						new_flags += 0x01
					case flaglist[1]:
						new_flags += 0x01 << 1
					case flaglist[2]:
						new_flags += 0x01 << 2
					case flaglist[3]:
						new_flags += 0x01 << 3
					case flaglist[4]:
						new_flags += 0x01 << 4
					}
				}
				if new_flags != indextickets[k].flags {
					fl := &FlagTicket{
						old_flags: indextickets[k].flags,
						new_flags: new_flags,
						digest:    id.indexbytes[indextickets[k].location][4 : digest_length+4],
					}
					id.indexbytes[indextickets[k].location][4+digest_length] = new_flags
					fl.WriteTo(path_buffer)
				}
			}
		}
		for _, it := range indextickets {
			if it.seen {
				continue
			}
			deleted++
			id.indexbytes[it.location][4+digest_length] = 0xff
			path := filepath.Join(*targetdir, fmt.Sprintf("%02x", id.indexbytes[it.location][4]), fmt.Sprintf("%02x", id.indexbytes[it.location][5:digest_length+4]))
			fmt.Fprintf(path_buffer, "+offline %s\n", path)
		}
		if deleted > 0 {
			if e := id.Sort(4 + digest_length); e != nil {
				panic(e)
			}
			end := sort.Search(len(id.indexbytes), func(i int) bool {
				return id.indexbytes[i][4+digest_length] == 0xff
			})
			id.indexbytes = id.indexbytes[:end]
			if e := id.Sort(5); e != nil {
				panic(e)
			}
		}
		if fetch_seq.Empty() {
			close(fetch)
			return
		} else {
			c.UidFetch(fetch_seq, canonical_header_fetch_items, fetch)
		}
	}()
	return fetch, c.Fetch(uid_seq, uid_fetch_items, uid_chan)
}

func (id *IndexData) FilterCanonicalHeaders(fetch chan *imap.Message, hasher hash.Hash, path_buffer *bytes.Buffer) (chan *imap.Message, error) {
	counter := 0
	num := 0
	full_fetch := new(imap.SeqSet)
	for msg := range fetch {
		if m, e := mail.ReadMessage(msg.GetBody(canonical_header_section)); e != nil {
			continue
		} else {
			if n, e := WriteHeaders(m.Header, hasher); e != nil {
				return nil, e
			} else {
				counter += n
			}
			rticket := new(ResponseTicket)
			rticket.headers = m.Header

			custom := make([]string, 0, 2)
			if id.k == 1 {
				custom = append(custom, "+sent")
			}

			for _, fl := range msg.Flags {
				switch fl {
				case flaglist[0]:
					rticket.flags += 0x01
				case flaglist[1]:
					rticket.flags += 0x01 << 1
				case flaglist[2]:
					rticket.flags += 0x01 << 2
				case flaglist[3]:
					rticket.flags += 0x01 << 3
				case flaglist[4]:
					rticket.flags += 0x01 << 4
				}
			}
			rticket.uid = msg.Uid
			copy(rticket.digest[:], hasher.Sum(nil))
			hasher.Reset()
			if e := rticket.Stat(*targetdir); e != nil {
				// need to full fetch
				full_fetch.AddNum(msg.Uid)
				num++
			}
			fl := &FlagTicket{
				old_flags: 0x00,
				new_flags: rticket.flags,
				digest:    rticket.digest[:],
				custom:    custom,
			}
			fl.WriteTo(path_buffer)
			var t [digest_length + 5]byte
			rticket.Read(t[:])
			id.indexbytes = append(id.indexbytes, t)
		}
	}
	if counter > 0 {
		fmt.Fprintf(os.Stderr, "h %s: %d b\n", filepath.Base(id.filename)[:5], counter)
		if e := id.Sort(5); e != nil {
			return nil, e
		}
	}
	if full_fetch.Empty() {
		return nil, nil
	}

	c := <-id.cc
	full := make(chan *imap.Message, num)
	go func() {
		if _, e := c.Select(id.mailboxname, false); e != nil {
			panic(e)
		} else if e := c.UidFetch(full_fetch, full_fetch_items, full); e != nil {
			panic(e)
		} else {
			id.cc <- c
		}
	}()
	return full, nil
}

func (id *IndexData) HandleFullFetched(full chan *imap.Message) error {
	c := <-id.cc
	defer func() {
		id.cc <- c
	}()
	tickets := make(chan *ArchiveTicket)
	batons := make(chan *ArchiveTicket, num_batons)
	go func() {
		if count, e := HandleArchiveTickets(*targetdir, tickets); e != nil {
			panic(e)
		} else {
			fmt.Fprintf(os.Stderr, "w %s: %0.4f MB\n", filepath.Base(id.filename)[:5], float64(count)/1000000)
		}
	}()
	for i := 0; i < num_batons; i++ {
		a := &ArchiveTicket{
			hasher:  sha256.New(),
			batons:  batons,
			tickets: tickets,
			file:    nil,
			msg:     new(mail.Message),
			rb:      nil,
			wb:      new(bufio.Writer),
		}
		batons <- a
	}
	for msg := range full {
		t := <-batons
		if body, e := io.ReadAll(msg.GetBody(full_section)); e != nil {
			return e
		} else if m, e := mail.ReadMessage(bytes.NewBuffer(body)); e != nil {
			return e
		} else {
			t.rb = body
			t.msg = m
			t.Submit()
		}
	}
	for i := 0; i < num_batons; i++ {
		<-batons
	}
	close(batons)
	close(tickets)
	return nil
}

func (id *IndexData) SaveIndexFile(wb *bufio.Writer) error {
	id.UidSort()
	if f, e := os.CreateTemp(filepath.Dir(id.filename), filepath.Base(id.filename)); e != nil {
		return e
	} else {
		wb.Reset(f)
		for _, id := range id.indexbytes {
			wb.Write(id[:])
		}
		if e := wb.Flush(); e != nil {
			return e
		} else if e := f.Close(); e != nil {
			return e
		} else if e := os.Rename(f.Name(), id.filename); e != nil {
			return e
		}
	}
	return nil
}

func (id *IndexData) Disconnect() error {
	c := <-id.cc
	select {
	case <-c.LoggedOut():
		id.cc <- c
		return nil
	default:
	}
	if e := c.Logout(); e != nil {
		return e
	} else {
		id.cc <- c
	}
	return nil
}

func (id *IndexData) ReadIndexFile() (e error) {
	var rb *bufio.Reader
	if f, e := os.Open(id.filename); e != nil {
		return e
	} else if i, e := f.Stat(); e != nil {
		return e
	} else {
		defer f.Close()
		rb = bufio.NewReaderSize(f, int(i.Size()))
		id.indexbytes = make([][digest_length + 5]byte, int(i.Size())/(digest_length+4+1))
	}
	for k := 0; k < len(id.indexbytes); k++ {
		rb.Read(id.indexbytes[k][:])
	}
	return nil
}

func (id *IndexData) Sort(start int) error {
	if start > digest_length+4 {
		return fmt.Errorf("start is OOB")
	}
	var end int
	if start < digest_length+4 {
		end = 4 + digest_length
	} else {
		end = 5 + digest_length
	}
	sort.Slice(id.indexbytes, func(i, j int) bool {
		for k, b := range id.indexbytes[i][start:end] {
			switch {
			case b <= id.indexbytes[j][start+k]:
				return true
			case b == id.indexbytes[j][start+k]:
				continue
			case b > id.indexbytes[j][start+k]:
				return false
			}
		}
		return true
	})
	return nil
}

func (id *IndexData) UidSort() {
	sort.Slice(id.indexbytes, func(i, j int) bool {
		return read_uint32(id.indexbytes[i][0:4]) < read_uint32(id.indexbytes[j][0:4])
	})
}

var NotFound = fmt.Errorf("not found")

func (id *IndexData) Search(start int, query []byte) (location int, result []byte, err error) {
	if len(query)+start > digest_length+5 {
		return 0, nil, fmt.Errorf("query is too long")
	}
	var terminal bool
	k := sort.Search(len(id.indexbytes), func(i int) bool {
		for k, b := range id.indexbytes[i][start : start+len(query)] {
			switch {
			case query[k] > b:
				return false
			case query[k] == b:
				continue
			case query[k] < b:
				return true
			}
		}
		terminal = true
		return true
	})
	if !terminal {
		return 0, nil, NotFound
	} else {
		return k, id.indexbytes[k][:], nil
	}
}
