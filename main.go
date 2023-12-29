package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"os/signal"
	"syscall"

	"golang.org/x/net/context"
)

const digest_length = 20
const num_batons = 4

var indexdir = flag.String("index", "mail/.index", "index file directory")
var targetdir = flag.String("t", "mail/target", "target directory")
var notmuchdir = flag.String("notmuch", "mail/.notmuch/xapian", "notmuch dir")
var lastmodfile = flag.String("lastmod", "mail/.lastmod", "lastmod file")
var portable = flag.Bool("p", false, "portable (not relative HOME)")
var printauth = flag.Bool("auth", false, "print out AUTH information")
var wait = flag.Int("wait", 60, "amount of time to wait")

func main() {
	flag.Parse()

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

	sorted_index_chan := make(chan *IndexData, size)


	// first in last out
	defer func() {
		timeout := time.Duration(*wait * 1000 * 1000 * 1000)
		fmt.Fprintf(os.Stderr, "waiting for %s\n", timeout)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			for range sigs {
				if ctx.Err() == nil {
					cancel()
				}
			}
		}()
		if e := LRFlagIdle(ctx, *lastmodfile, *notmuchdir, sorted_index_chan, size); e != nil {
			panic(e)
		}
	}()

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
