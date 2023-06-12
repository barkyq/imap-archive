package main

import (
	"crypto/sha1"
	"fmt"
)

func GenerateMailboxID(h string, a string, salt [digest_length]byte) string {
	hw := sha1.New()
	hw.Write([]byte(h))
	hw.Write([]byte(a))
	hw.Write(salt[:])
	return fmt.Sprintf("%x", hw.Sum(nil)[:5])
}
