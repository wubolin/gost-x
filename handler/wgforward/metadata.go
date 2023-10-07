package wgforward

//package local

import (
	"time"

	"github.com/go-gost/core/listener"
	mdata "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/core/metadata/util"
)

type metadata struct {
	readTimeout     time.Duration
	sniffing        bool
	sniffingTimeout time.Duration
	ln              listener.Listener
}

func (h *forwardHandler) parseMetadata(md mdata.Metadata) (err error) {
	const (
		readTimeout = "readTimeout"
		sniffing    = "sniffing"
	)

	h.md.readTimeout = mdutil.GetDuration(md, readTimeout)
	h.md.sniffing = mdutil.GetBool(md, sniffing)
	h.md.sniffingTimeout = mdutil.GetDuration(md, "sniffing.timeout")
	h.md.ln = md.Get("ln").(listener.Listener)
	return
}
