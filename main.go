package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const digest_length = 20
const num_batons = 4

var indexdir = flag.String("index", "mail/.index", "index file directory")
var targetdir = flag.String("t", "mail/target", "target directory")
var portable = flag.Bool("p", false, "portable (not relative HOME)")
var printauth = flag.Bool("auth", false, "print out AUTH information")
var no_notmuch = flag.Bool("no-notmuch", false, "do not call notmuch")

func main() {
	flag.Parse()

	// initialize portable directories
	if !*portable {
		if s, e := os.UserHomeDir(); e != nil {
			panic(e)
		} else {
			*targetdir = filepath.Join(s, *targetdir)
			*indexdir = filepath.Join(s, *indexdir)
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

	sorted_index_chan := make(chan *IndexData, size)

	// first in last out
	if !*printauth {
		defer func() {
			// no idle
			disconnect_chan := make(chan *IndexData, size)
			path_buffer := bytes.NewBuffer(nil)
			wb := new(bufio.Writer)
			for id := range sorted_index_chan {
				id.ForceUpdate(path_buffer)
				// blocks until done
				c := <-id.cc

				// save index file
				if e := id.SaveIndexFile(wb); e != nil {
					panic(e)
				}
				// puts back into chan in case someone else needs client
				id.cc <- c
				disconnect_chan <- id
			}

			if !*no_notmuch {
				if e := UpdateNotmuch(path_buffer); e != nil {
					panic(e)
				}
			}

			close(disconnect_chan)
			for id := range disconnect_chan {
				if e := id.Disconnect(); e != nil {
					panic(e)
				}
			}
		}()
	}

	unsorted_index_chan := make(chan *IndexData)

	var index_wg sync.WaitGroup
	index_wg.Add(1)
	go func() {
		defer index_wg.Done()
		for id := range unsorted_index_chan {
			id.Sort(5) // sort on the 5th byte
			sorted_index_chan <- id
		}
		close(sorted_index_chan)
	}()
	defer index_wg.Wait()

	config_chan := make(chan *Config, len(conf_paths))
	for _, cp := range conf_paths {
		if c, e := HandleConfInit(cp); e != nil {
			fmt.Fprintf(os.Stderr, e.Error())
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
				InitHandler(cc, conf, unsorted_index_chan)
			}
		}(conf)
	}
	mwg.Wait()
	close(unsorted_index_chan)
}
