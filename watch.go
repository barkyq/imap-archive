package main

import (
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

func Watch(dir string) (local_notifs chan string, err error) {
	local_notifs = make(chan string)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	if e := watcher.Add(dir); e != nil {
		return nil, e
	}
	go func() {
		for {
			if ev, ok := <-watcher.Events; !ok {
				panic(ok)
			} else if ev.Name == filepath.Join(dir, "iamglass") {
				local_notifs <- fmt.Sprintf(ev.String())
			}
		}
	}()
	return local_notifs, nil
}
