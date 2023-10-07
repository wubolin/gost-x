package wgforward

//package local

//modify memo
//0. change init
//1. import  xnet/auth_util  change local
//2. disable handleHTTP
//3. change forwardHandler to ForwardHandler
//4. add chainConn chan net.Conn; ClosedEvent chan struct{}; conn  net.Conn to ForwardHandler
//5. disable dial
//6. close lastconn, update lastconn
//7. NewHandle 中的修改

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/hop"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	xnet "github.com/go-gost/x/internal/net"
	auth_util "github.com/go-gost/x/internal/util/auth"
	"github.com/go-gost/x/internal/util/forward"
	"github.com/go-gost/x/registry"
)

func init() {
	//wbl 2023-10-03 21:00 begin
	// registry.HandlerRegistry().Register("tcp", NewHandler)
	// registry.HandlerRegistry().Register("udp", NewHandler)
	// registry.HandlerRegistry().Register("forward", NewHandler)
	registry.HandlerRegistry().Register("wg-tcp", NewHandler)
	registry.HandlerRegistry().Register("wg-udp", NewHandler)
	registry.HandlerRegistry().Register("wg-forward", NewHandler)
	//wbl 2023-10-03 21:00 end
}

type forwardHandler struct {
	hop     hop.Hop
	router  *chain.Router
	md      metadata
	options handler.Options
	//wbl 2023-10-03 21:00 begin
	chainConn   chan net.Conn
	ClosedEvent chan struct{}
	conn        net.Conn //last conn
	//wbl 2023-10-03 21:00 end
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	//wbl 2023-09-29 19:12 begin
	// return &ForwardHandler{
	// 	options: options,
	// }
	h := &forwardHandler{
		options:     options,
		ClosedEvent: make(chan struct{}, 1),
		chainConn:   make(chan net.Conn, 1),
	}

	return h
	//wbl 2023-09-29 19:12 end
}

func (h *forwardHandler) Init(md md.Metadata) (err error) {
	if err = h.parseMetadata(md); err != nil {
		return
	}

	h.router = h.options.Router
	if h.router == nil {
		h.router = chain.NewRouter(chain.LoggerRouterOption(h.options.Logger))
	}

	return
}

// Forward implements handler.Forwarder.
func (h *forwardHandler) Forward(hop hop.Hop) {
	h.hop = hop
}

func (h *forwardHandler) Handle(ctx context.Context, conn net.Conn, opts ...handler.HandleOption) error {
	defer conn.Close()
	// wbl 2023-09-29 19:12 begin
	if h.conn != nil {
		h.conn.Close()
	}
	h.conn = conn
	connClosed := getClosedChanOfConn(conn)

	var cc net.Conn = nil
	select {
	case cc = <-h.chainConn:
	case <-connClosed:
	}
	if cc == nil {
		return errors.New("no remote conn")
	}
	defer cc.Close()
	//wbl 2023-09-29 19:12 end

	start := time.Now()
	log := h.options.Logger.WithFields(map[string]any{
		"remote": conn.RemoteAddr().String(),
		"local":  conn.LocalAddr().String(),
	})

	log.Infof("%s <> %s", conn.RemoteAddr(), conn.LocalAddr())
	defer func() {
		log.WithFields(map[string]any{
			"duration": time.Since(start),
		}).Infof("%s >< %s", conn.RemoteAddr(), conn.LocalAddr())
	}()

	if !h.checkRateLimit(conn.RemoteAddr()) {
		return nil
	}

	network := "tcp"
	if _, ok := conn.(net.PacketConn); ok {
		network = "udp"
	}

	ctx = auth_util.ContextWithClientAddr(ctx, auth_util.ClientAddr(conn.RemoteAddr().String()))

	var rw io.ReadWriter = conn
	var host string
	var protocol string
	if network == "tcp" && h.md.sniffing {
		if h.md.sniffingTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(h.md.sniffingTimeout))
		}
		rw, host, protocol, _ = forward.Sniffing(ctx, conn)
		log.Debugf("sniffing: host=%s, protocol=%s", host, protocol)
		if h.md.sniffingTimeout > 0 {
			conn.SetReadDeadline(time.Time{})
		}
	}

	if protocol == forward.ProtoHTTP {
		h.handleHTTP(ctx, rw, log)
		return nil
	}

	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "0")
	}

	var target *chain.Node
	if host != "" {
		target = &chain.Node{
			Addr: host,
		}
	}
	if h.hop != nil {
		target = h.hop.Select(ctx,
			hop.HostSelectOption(host),
			hop.ProtocolSelectOption(protocol),
		)
	}
	if target == nil {
		err := errors.New("target not available")
		log.Error(err)
		return err
	}

	log = log.WithFields(map[string]any{
		"host": host,
		"node": target.Name,
		"dst":  fmt.Sprintf("%s/%s", target.Addr, network),
	})

	log.Debugf("%s >> %s", conn.RemoteAddr(), target.Addr)

	addr := target.Addr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr += ":0"
	}
	//wbl 2023-09-29 19:12 begin
	// cc, err := h.router.Dial(ctx, network, addr)
	// if err != nil {
	// 	log.Error(err)
	// 	// TODO: the router itself may be failed due to the failed node in the router,
	// 	// the dead marker may be a wrong operation.
	// 	if marker := target.Marker(); marker != nil {
	// 		marker.Mark()
	// 	}
	// 	return err
	// }
	// defer cc.Close()
	//wbl 2023-09-29 19:12 end
	if marker := target.Marker(); marker != nil {
		marker.Reset()
	}

	t := time.Now()
	log.Infof("%s <-> %s", conn.RemoteAddr(), target.Addr)
	xnet.Transport(rw, cc)
	log.WithFields(map[string]any{
		"duration": time.Since(t),
	}).Infof("%s >-< %s", conn.RemoteAddr(), target.Addr)

	return nil
}

func (h *forwardHandler) handleHTTP(ctx context.Context, rw io.ReadWriter, log logger.Logger) (err error) {
	br := bufio.NewReader(rw)
	var connPool sync.Map

	for {
		resp := &http.Response{
			ProtoMajor: 1,
			ProtoMinor: 1,
			StatusCode: http.StatusServiceUnavailable,
		}

		err = func() error {
			req, err := http.ReadRequest(br)
			if err != nil {
				return err
			}

			target := &chain.Node{
				Addr: req.Host,
			}
			if h.hop != nil {
				target = h.hop.Select(ctx,
					hop.HostSelectOption(req.Host),
					hop.ProtocolSelectOption(forward.ProtoHTTP),
				)
			}
			if target == nil {
				log.Warnf("node for %s not found", req.Host)
				resp.StatusCode = http.StatusBadGateway
				return resp.Write(rw)
			}

			log = log.WithFields(map[string]any{
				"host": req.Host,
				"node": target.Name,
				"dst":  target.Addr,
			})
			log.Debugf("find node for host %s -> %s(%s)", req.Host, target.Name, target.Addr)

			if auther := target.Options().Auther; auther != nil {
				username, password, _ := req.BasicAuth()
				id, ok := auther.Authenticate(ctx, username, password)
				if !ok {
					resp.StatusCode = http.StatusUnauthorized
					resp.Header.Set("WWW-Authenticate", "Basic")
					log.Warnf("node %s(%s) 401 unauthorized", target.Name, target.Addr)
					return resp.Write(rw)
				}
				ctx = auth_util.ContextWithID(ctx, auth_util.ID(id))
			}

			var cc net.Conn
			if v, ok := connPool.Load(target); ok {
				cc = v.(net.Conn)
				log.Debugf("connection to node %s(%s) found in pool", target.Name, target.Addr)
			}
			if cc == nil {
				cc, err = h.router.Dial(ctx, "tcp", target.Addr)
				if err != nil {
					// TODO: the router itself may be failed due to the failed node in the router,
					// the dead marker may be a wrong operation.
					if marker := target.Marker(); marker != nil {
						marker.Mark()
					}
					log.Warnf("connect to node %s(%s) failed: %v", target.Name, target.Addr, err)
					return resp.Write(rw)
				}
				if marker := target.Marker(); marker != nil {
					marker.Reset()
				}

				if tlsSettings := target.Options().TLS; tlsSettings != nil {
					cc = tls.Client(cc, &tls.Config{
						ServerName:         tlsSettings.ServerName,
						InsecureSkipVerify: !tlsSettings.Secure,
					})
				}

				connPool.Store(target, cc)
				log.Debugf("new connection to node %s(%s)", target.Name, target.Addr)

				go func() {
					defer cc.Close()
					err := xnet.CopyBuffer(rw, cc, 8192)
					if err != nil {
						resp.Write(rw)
					}
					log.Debugf("close connection to node %s(%s), reason: %v", target.Name, target.Addr, err)
					connPool.Delete(target)
				}()
			}

			if httpSettings := target.Options().HTTP; httpSettings != nil {
				if httpSettings.Host != "" {
					req.Host = httpSettings.Host
				}
				for k, v := range httpSettings.Header {
					req.Header.Set(k, v)
				}
			}

			if log.IsLevelEnabled(logger.TraceLevel) {
				dump, _ := httputil.DumpRequest(req, false)
				log.Trace(string(dump))
			}
			if err := req.Write(cc); err != nil {
				log.Warnf("send request to node %s(%s) failed: %v", target.Name, target.Addr, err)
				return resp.Write(rw)
			}

			if req.Header.Get("Upgrade") == "websocket" {
				err := xnet.CopyBuffer(cc, br, 8192)
				if err == nil {
					err = io.EOF
				}
				return err
			}

			// cc.SetReadDeadline(time.Now().Add(10 * time.Second))

			return nil
		}()
		if err != nil {
			// log.Error(err)
			break
		}
	}

	connPool.Range(func(key, value any) bool {
		if value != nil {
			value.(net.Conn).Close()
		}
		return true
	})

	return
}

func (h *forwardHandler) checkRateLimit(addr net.Addr) bool {
	if h.options.RateLimiter == nil {
		return true
	}
	host, _, _ := net.SplitHostPort(addr.String())
	if limiter := h.options.RateLimiter.Limiter(host); limiter != nil {
		return limiter.Allow(1)
	}

	return true
}
