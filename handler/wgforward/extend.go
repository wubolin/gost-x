package wgforward

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"strings"
	"syscall"
	"unsafe"

	"github.com/go-gost/core/handler"
	"github.com/gorilla/websocket"
)

type GetRawConn interface {
	SyscallConn() (syscall.RawConn, error)
}

type ChainConn struct {
	net.Conn
	h      *forwardHandler
	closed chan struct{}
}

func (c *ChainConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if err != nil {
		c.Close()
	}
	return
}

func (c *ChainConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err != nil {
		c.Close()
	}
	return
}

func (c *ChainConn) Close() (err error) {
	select {
	case <-c.closed:
	default:
		close(c.closed)
		c.h.ClosedEvent <- struct{}{}
	}
	return c.Conn.Close()
}

func Dial(wgH handler.Handler, addr string) (fd uintptr, err error) {
	h, ok := wgH.(*forwardHandler)

	if !ok {
		err = errors.New("handler is not wgforward handler")
		return
	}

	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr += ":0"
	}

	conn, err := h.router.Dial(context.Background(), "udp", addr)
	if err != nil {
		return
	}

	fd, err = GetConnFD(conn)
	if err != nil {
		return
	}

	h.chainConn <- &ChainConn{
		Conn:   conn,
		h:      h,
		closed: make(chan struct{}),
	}
	return
}

func GetClosed(wgH handler.Handler) (closed chan struct{}, err error) {
	h, ok := wgH.(*forwardHandler)

	if !ok {
		err = errors.New("handler is not wgforward handler")
		return
	}

	closed = h.ClosedEvent
	return
}

func GetConnFD(conn net.Conn) (uintptr, error) {
	var fdTCP uintptr
	var err = errors.New("no connection")

	var rawConn GetRawConn

	if conn != nil {
		var obj reflect.Value = reflect.ValueOf(conn)
		var t reflect.Type
		var kind reflect.Kind
		var name string
	Outloop:
		for {
			t = obj.Type()
			name = t.String()
			kind = t.Kind()
			switch kind {
			case reflect.Pointer:
				switch {
				case strings.Contains(name, "TCPConn"):
					rawConn = (*net.TCPConn)(obj.UnsafePointer())
					break Outloop
				case strings.Contains(name, "UDPConn"):
					rawConn = (*net.UDPConn)(obj.UnsafePointer())
					break Outloop
				case strings.Contains(name, "websocket.Conn"):
					webConn := (*websocket.Conn)(obj.UnsafePointer())
					obj = reflect.ValueOf(webConn.UnderlyingConn())
				case strings.Contains(name, "tls.Conn"):
					tlsConn := (*tls.Conn)(obj.UnsafePointer())
					obj = reflect.ValueOf(tlsConn.NetConn())
				case strings.Contains(name, "smux.Stream"):
					s := obj.Elem()
					obj = s.FieldByName("sess")
				default:
					obj = obj.Elem()
				}

			case reflect.Interface:
				obj = obj.Elem()
			case reflect.Struct:
				obj = obj.FieldByNameFunc(func(name string) bool {
					if name == "Conn" || name == "conn" {
						return true
					} else {
						return false
					}
				})
			default:
				break Outloop
			}
		}

		if rawConn != nil {
			var raw syscall.RawConn
			raw, err = rawConn.SyscallConn()
			if err == nil {
				err = raw.Control(func(fd uintptr) {
					fdTCP = fd
				})
			}
		}
	}

	return fdTCP, err
}

func getClosedChanOfConn(conn net.Conn) chan struct{} {
	value := reflect.ValueOf(conn).Elem().FieldByName("closed")

	x := reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()

	return x.Interface().(chan struct{})
}

// when conn close ,delete conn from pool of conn in listener  deprecated
// func (h *forwardHandler) patchOfConn(conn net.Conn) {
// 	vClosed := reflect.ValueOf(conn).Elem().FieldByName("closed")
// 	closed := reflect.NewAt(vClosed.Type(), unsafe.Pointer(vClosed.UnsafeAddr())).Elem().Interface().(chan struct{})

// 	vKey := reflect.ValueOf(conn).Elem().FieldByName("remoteAddr")
// 	key := (reflect.NewAt(vKey.Type(), unsafe.Pointer(vKey.UnsafeAddr())).Elem().Interface().(net.Addr)).String()

// 	vPool := reflect.ValueOf(h.md.ln).Elem().FieldByName("ln").Elem().Elem().FieldByName("connPool")
// 	pDelete := reflect.NewAt(vPool.Type(), unsafe.Pointer(vPool.UnsafeAddr())).Elem().MethodByName("Delete")

// 	go func() {
// 		<-closed

// 		pDelete.Call([]reflect.Value{
// 			reflect.ValueOf(key),
// 		})
// 	}()
// }
