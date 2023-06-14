package main

import (
	"sort"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func LocalToRemoteFlag(client_chan chan *client.Client, uids_update_chan chan uint32, flag string, results chan *imap.Message) error {
	c := <-client_chan
	uids_seen := new(imap.SeqSet)
	for x := range uids_update_chan {
		uids_seen.AddNum(x)
	}
	if uids_seen.Empty() {
		client_chan <- c
		return nil
	}
	ch := make(chan *imap.Message)
	go func() {
		ids_seen := new(imap.SeqSet)
		for m := range ch {
			ids_seen.AddNum(m.SeqNum)
		}
		flags := []interface{}{flag}
		if ids_seen.Empty() {
			client_chan <- c
			return
		}
		if e := c.Store(ids_seen, imap.FormatFlagsOp(imap.AddFlags, false), flags, results); e != nil {
			panic(e)
		} else {
			client_chan <- c
		}
	}()
	return c.UidFetch(uids_seen, []imap.FetchItem{imap.FetchUid}, ch)
}

func Check(index []byte, filter [][digest_length - 1]byte) bool {
	if len(index) < digest_length-1 {
		return false
	}
	var terminal bool
	sort.Search(len(filter), func(i int) bool {
		for k, b := range filter[i] {
			switch {
			case index[k] > b:
				return false
			case index[k] == b:
				continue
			case index[k] < b:
				return true
			}
		}
		terminal = true
		return true
	})
	return terminal
}

func Filter(filter [][digest_length - 1]byte, indexbytes [][digest_length + 5]byte, antifilter bool, mask byte, uids_update_chan chan uint32) {
	defer close(uids_update_chan)
	if antifilter && len(filter) == 0 {
		// nothing has antiflag, so put flag to everything
		for _, ibs := range indexbytes {
			uid := uint32(ibs[0]) + uint32(ibs[1])*256 + uint32(ibs[2])*65536 + uint32(ibs[3])*16777216
			uids_update_chan <- uid
		}
	}
	uch, ich := make(chan []byte, num_batons), make(chan []byte, num_batons)
	for i := 0; i < num_batons; i++ {
		uch <- make([]byte, 4)
		ich <- make([]byte, digest_length-1)
	}
	for _, ibs := range indexbytes {
		if (ibs[digest_length+4]/mask)%2 == 0x01 {
			// already has flag
			continue
		}
		u := <-uch
		copy(u, ibs[:4])
		i := <-ich
		copy(i, ibs[5:digest_length+4])
		go func(uids []byte, ibs []byte) {
			defer func() {
				uch <- uids
				ich <- ibs
			}()
			if terminal := Check(ibs, filter); (!terminal && antifilter) || (terminal && !antifilter) {
				// antifilter and not found, means it gets tag, or
				// filter and found, means it gets tag
				uid := uint32(uids[0]) + uint32(uids[1])*256 + uint32(uids[2])*65536 + uint32(uids[3])*16777216
				uids_update_chan <- uid
			}
		}(u, i)
	}
	for i := 0; i < num_batons; i++ {
		<-uch
		<-ich
	}
}
