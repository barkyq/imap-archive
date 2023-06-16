package main

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"sort"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type IndexData struct {
	mailboxname  string
	filename     string
	indexbytes   [][digest_length + 5]byte
	updatebuffer [][5]byte
	cc           chan *client.Client
}

func (id *IndexData) SubmitFlags() error {
	uids := make([]*imap.SeqSet, 4)
	for _, x := range id.updatebuffer {
		for k := range uids {
			if x[0]/(0x02<<k)%0x02 == 1 {
				if uids[k] == nil {
					uids[k] = new(imap.SeqSet)
				}
				uids[k].AddNum(uint32(x[1]) + uint32(x[2])*256 + uint32(x[3])*65536 + uint32(x[4])*16777216)
			}
		}
	}
	c := <-id.cc
	defer func() {
		id.cc <- c
	}()
	if _, e := c.Select(id.mailboxname, false); e != nil {
		return e
	}
	for k, u := range uids {
		if e := func(k int, u *imap.SeqSet) error {
			var wg sync.WaitGroup
			defer wg.Wait()
			if u == nil || u.Empty() {
				return nil
			}
			ch := make(chan *imap.Message)
			wg.Add(1)
			go func() {
				defer wg.Done()
				ids_seen := new(imap.SeqSet)
				for m := range ch {
					ids_seen.AddNum(m.SeqNum)
				}
				flags := []interface{}{flaglist[k]}
				if ids_seen.Empty() {
					return
				}
				if e := c.Store(ids_seen, imap.FormatFlagsOp(imap.AddFlags, false), flags, nil); e != nil {
					panic(e)
				}
			}()
			return c.UidFetch(u, []imap.FetchItem{imap.FetchUid}, ch)
		}(k, u); e != nil {
			return e
		}
	}
	return nil
}

func ReadIndexFile(mailboxname string, indexfile string) (indexdata *IndexData, e error) {
	var rb *bufio.Reader
	indexdata = new(IndexData)
	indexdata.mailboxname = mailboxname
	if f, e := os.Open(indexfile); e != nil {
		return nil, e
	} else if i, e := f.Stat(); e != nil {
		return nil, e
	} else {
		defer f.Close()
		indexdata.filename = indexfile
		rb = bufio.NewReaderSize(f, int(i.Size()))
		indexdata.indexbytes = make([][digest_length + 5]byte, int(i.Size())/(digest_length+4+1))
	}
	for k := 0; k < len(indexdata.indexbytes); k++ {
		rb.Read(indexdata.indexbytes[k][:])
	}
	return indexdata, nil
}

func (id *IndexData) Sort(start int) error {
	if start > digest_length+4 {
		return fmt.Errorf("start is OOB")
	}
	sort.Slice(id.indexbytes, func(i, j int) bool {
		for k, b := range id.indexbytes[i][start : 4+digest_length] {
			switch {
			case b < id.indexbytes[j][start+k]:
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

var NotFound = fmt.Errorf("not found")

func (id *IndexData) Search(start int, query []byte) (result []byte, err error) {
	if len(query)+start > digest_length+5 {
		return nil, fmt.Errorf("query is too long")
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
		return nil, NotFound
	} else {
		return id.indexbytes[k][:], nil
	}
}
