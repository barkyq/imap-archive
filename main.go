package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const digest_length = 20
const num_batons = 4

var indexdir = flag.String("index", "mail/.index", "index file directory")
var targetdir = flag.String("t", "mail/target", "target directory")
var notmuchdir = flag.String("notmuch", "mail/.notmuch/xapian", "notmuch dir")
var lastmodfile = flag.String("lastmod", "mail/.lastmod", "lastmod file")
var portable = flag.Bool("p", false, "portable (not relative HOME)")
var verbose = flag.Bool("v", true, "be verbose")

func main() {
	flag.Parse()

	var logger io.Writer
	if *verbose {
		logger = os.Stderr
	} else if f, e := os.CreateTemp("/tmp", "imap-archive_log_"); e != nil {
		panic(e)
	} else {
		fmt.Fprintf(os.Stderr, "logging to %s\n", f.Name())
		defer f.Close()
		logger = f
	}

	// initialize portable directories
	if !*portable {
		if s, e := os.UserHomeDir(); e != nil {
			panic(e)
		} else {
			*targetdir = filepath.Join(s, *targetdir)
			*indexdir = filepath.Join(s, *indexdir)
			*notmuchdir = filepath.Join(s, *notmuchdir)
			*lastmodfile = filepath.Join(s, *lastmodfile)
		}
	}

	// initialize index directory
	if e := os.MkdirAll(*indexdir, os.ModePerm); e != nil {
		panic(e)
	}

	conf_paths, size := ParseConfInit(os.Stdin)

	// at the very end, update notmuch tags
	// write valid paths which will contain messages to path_buffer
	// one on each line
	path_buffer := bytes.NewBuffer(nil)
	maybe_delete_buffer := bytes.NewBuffer(nil)

	sorted_index_chan := make(chan *IndexData, size)

	defer func() {
		if e := LRFlagIdle(*lastmodfile, *notmuchdir, sorted_index_chan, size); e != nil {
			panic(e)
		}
	}()

	// TODO local -> remote flag idle syncing
	// defer LRSync()
	defer UpdateNotmuch(path_buffer)

	unsorted_index_chan := make(chan *IndexData)
	full_index_buffer := bytes.NewBuffer(nil)
	var index_wg sync.WaitGroup
	defer MaybeDeleteHandler(*targetdir, full_index_buffer, maybe_delete_buffer, path_buffer)
	index_wg.Add(1)
	go func() {
		defer index_wg.Done()
		for id := range unsorted_index_chan {
			for _, v := range id.indexbytes {
				if n, e := full_index_buffer.Write(v[4 : digest_length+4]); e != nil || n != digest_length {
					panic(e)
				}
			}
			id.Sort(5) // sort on the 5th byte
			sorted_index_chan <- id
		}
		close(sorted_index_chan)
	}()
	defer index_wg.Wait()

	config_chan := make(chan *Config, len(conf_paths))
	for _, cp := range conf_paths {
		if c, e := HandleConfInit(cp); e != nil {
			fmt.Fprintf(logger, e.Error())
		} else {
			config_chan <- c
		}
	}
	close(config_chan)

	var mwg sync.WaitGroup
	for conf := range config_chan {
		mwg.Add(1)
		go func(conf *Config) {
			defer mwg.Done()
			if cc, e := conf.InitClient(); e != nil {
				panic(e)
			} else {
				FetchHandler(cc, conf, logger, unsorted_index_chan, path_buffer, maybe_delete_buffer)
			}
		}(conf)
	}
	mwg.Wait()
	close(unsorted_index_chan)
}
