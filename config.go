package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/emersion/go-sasl"
)

// LoadConfig loads a configuration file (json encoded) and returns the relevant information.
// addr (hostname:port format) is the remote address for which to make a connection.
// folder_list (map[local_name]remote_name) is the list of folders for which to sync; default values are "inbox", "sent", and "archive".
// directory is the root directory containing the maildir
// mem represents the local representation of the mailbox
func LoadConfig(r io.Reader) (addr string, a sasl.Client, directory string, e error) {
	userinfo := make(map[string]string)

	// load config from os.Stdin
	dec := json.NewDecoder(r)
	if e = dec.Decode(&userinfo); e != nil {
		return
	}
	directory = userinfo["directory"]
	os.MkdirAll(directory, os.ModePerm)

	addr = userinfo["imap_server"]
	switch userinfo["type"] {
	case "plain":
		a = sasl.NewPlainClient("", userinfo["user"], userinfo["password"])
	}
	return
}
