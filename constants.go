package main

import (
	"time"

	"github.com/emersion/go-imap"
)

var canonical_header_list = []string{
	"Message-ID",
}

var timeout = 180 * time.Second
var countdown = 30 * time.Second

var header_list = []string{
	"From",
	"To",
	"Cc",
	"Subject",
	"In-Reply-To",
	"References",
	"Date",
	"Message-ID",
	"MIME-Version",
	"Content-Type",
	"Content-Disposition",
	"Content-Transfer-Encoding",
	"Hash",
}

// local
var taglist = []string{"unread", "replied", "deleted", "forwarded", "flagged"}

// remote
var flaglist = []string{"\\Seen", "\\Answered", "\\Deleted", "$Forwarded", "\\Flagged"}

// section: canonical headers for hashing
var canonical_header_section = &imap.BodySectionName{
	BodyPartName: imap.BodyPartName{
		Specifier: imap.HeaderSpecifier,
		Fields:    canonical_header_list,
	},
	Peek: true,
}

// section: full message
var full_section = &imap.BodySectionName{
	Peek: true,
}

// fetch items: just uids and flags
var uid_fetch_items = []imap.FetchItem{
	imap.FetchUid,
	imap.FetchFlags,
}

// fetch items: canonical_header
var canonical_header_fetch_items = []imap.FetchItem{
	canonical_header_section.FetchItem(),
	imap.FetchUid,
	imap.FetchFlags,
}

// fetch_items: full message
var full_fetch_items = []imap.FetchItem{
	full_section.FetchItem(),
}
