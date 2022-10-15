package client

import (
	"fmt"
	"net/http"
	"solar-ev-charger/chargers/common"

	"github.com/pkg/errors"
)

func NewChargerClient(addr string) common.Client {
	return &httpClient{
		addr: addr,
	}
}

type httpClient struct {
	addr string
}

func (h *httpClient) doGet(setting string, value uint64) error {
	uri := fmt.Sprintf("http://%s/mqtt?payload=%s=%d", h.addr, setting, value)
	resp, err := http.Get(uri)
	if err != nil {
		return errors.Wrap(err, "sending request")
	}
	defer resp.Body.Close()
	return nil
}

func (h *httpClient) Start() error {
	return h.doGet("alw", 1)
}

func (h *httpClient) Stop() error {
	return h.doGet("alw", 0)
}

func (h *httpClient) SetAmp(amp uint64) error {
	return h.doGet("amp", amp)
}
