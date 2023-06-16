package main

import (
	"errors"
	"os"
)

func LRFlagIdle(lastmodfile string, notmuchdir string, sic chan *IndexData, size int) error {
	ids := make([]*IndexData, 0, size)
	for id := range sic {
		id.updatebuffer = make([][5]byte, 0, 16)
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
			buffer, e := GetNotmuchTags(taglist, nil, uuid, lastmod)
			if e != nil {
				panic(e)
			}
			for _, id := range ids {
				for k, buf := range buffer {
					for _, query := range buf {
						if result, e := id.Search(5, query[:]); errors.Is(e, NotFound) {
							continue
						} else if e != nil {
							return e
						} else if (result[digest_length+4]/(0x02<<k))%0x02 == 0x01 {
							continue
						} else {
							var tmp [5]byte
							tmp[0] = 0x02 << k
							result[digest_length+4] += 0x02 << k
							copy(tmp[1:], result[:4])
							id.updatebuffer = append(id.updatebuffer, tmp)
						}
					}
				}
				if len(id.updatebuffer) != 0 {
					if e := id.SubmitFlags(); e != nil {
						return e
					} else {
						id.updatebuffer = id.updatebuffer[:0]
					}
				}
			}
			<-ln
			if u, l, e := LastMod(); e != nil {
				return e
			} else {
				uuid = u
				lastmod = l
			}
		}
	}
}
