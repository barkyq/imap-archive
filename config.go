package main

import (
	"encoding/base64"
	"encoding/json"
	"io"

	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/emersion/go-sasl"
)

type Config struct {
	filename string
	folders  []string
	r        io.ReadCloser // decrypted
	salt     []byte
	addr     string
	a        sasl.Client
}

type UserInfo struct {
	SMTPServer   string `json:"smtp_server"`
	IMAPServer   string `json:"imap_server"`
	Type         string `json:"type"`
	User         string `json:"user"`
	Password     string `json:"password"`
	ClientID     string `json:"clientid"`
	ClientSecret string `json:"clientsecret"`
	RefreshToken string `json:"refreshtoken"`
}

func (c *Config) PrintAuth(m string, ir []byte) string {
	return fmt.Sprintf("%s %s %s %s", GenerateMailboxID(c.folders[0], c.addr, c.salt), c.addr, m, base64.URLEncoding.EncodeToString(ir))
}

// LoadConfig loads a configuration file (json encoded) and returns the relevant information.
func LoadConfig(r io.Reader) (salt, addr string, a sasl.Client, e error) {
	userinfo := make(map[string]string)

	// load config from os.Stdin
	dec := json.NewDecoder(r)
	if e = dec.Decode(&userinfo); e != nil {
		return
	}
	// directory = userinfo["directory"]
	// os.MkdirAll(directory, os.ModePerm)

	salt = userinfo["salt"]
	addr = userinfo["imap_server"]
	switch userinfo["type"] {
	case "plain":
		a = sasl.NewPlainClient("", userinfo["user"], userinfo["password"])
	case "gmail":
		config, token := Gmail_Generate_Token(userinfo["clientid"], userinfo["clientsecret"], userinfo["refreshtoken"])
		a = XOAuth2(userinfo["user"], config, token)
	case "outlook":
		config, token := Outlook_Generate_Token(userinfo["clientid"], userinfo["refreshtoken"])
		a = XOAuth2(userinfo["user"], config, token)
	}
	return
}

func HandleConfInit(cp []string) (c *Config, e error) {
	if f, e := os.Open(cp[0]); e != nil {
		return nil, fmt.Errorf("ignoring: %s\n", cp[0])
	} else if !strings.HasSuffix(f.Name(), ".gpg") {
		return &Config{cp[0], cp[1:], f, nil, "", nil}, nil
	} else {
		// has gpg suffix
		cmd := exec.Command("/usr/bin/gpg", "-qd", "-")
		cmd.Stdin = f
		rp, wp := io.Pipe()
		cmd.Stdout = wp

		go func() {
			defer f.Close()
			if e := cmd.Run(); e != nil {
				panic(e)
			}
		}()
		return &Config{cp[0], cp[1:], rp, nil, "", nil}, nil
	}
}

func ParseConfInit(r io.Reader) ([][]string, int) {
	var size int
	cp := make([][]string, 0, 4)
	stdin := bufio.NewReader(r)
	for {
		if l, p, e := stdin.ReadLine(); e != nil {
			break
		} else if p {
			panic("isPrefix")
		} else {
			size += len(strings.Split(fmt.Sprintf("%s", l), ",")) - 1
			cp = append(cp, strings.Split(fmt.Sprintf("%s", l), ","))
		}
	}
	return cp, size
}
