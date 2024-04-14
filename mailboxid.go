package main

import (
	"crypto/sha1"
	"encoding/base64"
)

func GenerateMailboxID(mailbox string, addr string, salt []byte) string {
	hw := sha1.New()
	hw.Write([]byte(mailbox))
	hw.Write([]byte(addr))

	hw.Write(salt)
	// should be eight characters
	return base64.URLEncoding.EncodeToString(hw.Sum(nil)[:6])
}
