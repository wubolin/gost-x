package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-gost/core/ingress"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/plugin/ingress/proto"
	"github.com/go-gost/x/internal/plugin"
	"google.golang.org/grpc"
)

type grpcPlugin struct {
	conn   grpc.ClientConnInterface
	client proto.IngressClient
	log    logger.Logger
}

// NewGRPCPlugin creates an Ingress plugin based on gRPC.
func NewGRPCPlugin(name string, addr string, opts ...plugin.Option) ingress.Ingress {
	var options plugin.Options
	for _, opt := range opts {
		opt(&options)
	}

	log := logger.Default().WithFields(map[string]any{
		"kind":    "ingress",
		"ingress": name,
	})
	conn, err := plugin.NewGRPCConn(addr, &options)
	if err != nil {
		log.Error(err)
	}

	p := &grpcPlugin{
		conn: conn,
		log:  log,
	}
	if conn != nil {
		p.client = proto.NewIngressClient(conn)
	}
	return p
}

func (p *grpcPlugin) Get(ctx context.Context, host string) string {
	if p.client == nil {
		return ""
	}

	r, err := p.client.Get(ctx,
		&proto.GetRequest{
			Host: host,
		})
	if err != nil {
		p.log.Error(err)
		return ""
	}
	return r.GetEndpoint()
}

func (p *grpcPlugin) Close() error {
	if closer, ok := p.conn.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type httpPluginRequest struct {
	Host string `json:"host"`
}

type httpPluginResponse struct {
	Endpoint string `json:"endpoint"`
}

type httpPlugin struct {
	url    string
	client *http.Client
	header http.Header
	log    logger.Logger
}

// NewHTTPPlugin creates an Ingress plugin based on HTTP.
func NewHTTPPlugin(name string, url string, opts ...plugin.Option) ingress.Ingress {
	var options plugin.Options
	for _, opt := range opts {
		opt(&options)
	}

	return &httpPlugin{
		url:    url,
		client: plugin.NewHTTPClient(&options),
		header: options.Header,
		log: logger.Default().WithFields(map[string]any{
			"kind":    "ingress",
			"ingress": name,
		}),
	}
}

func (p *httpPlugin) Get(ctx context.Context, host string) (endpoint string) {
	if p.client == nil {
		return
	}

	rb := httpPluginRequest{
		Host: host,
	}
	v, err := json.Marshal(&rb)
	if err != nil {
		return
	}

	req, err := http.NewRequest(http.MethodPost, p.url, bytes.NewReader(v))
	if err != nil {
		return
	}

	if p.header != nil {
		req.Header = p.header.Clone()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	res := httpPluginResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return
	}
	return res.Endpoint
}
