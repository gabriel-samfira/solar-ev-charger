package client

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"solar-ev-charger/chargers/common"

	"github.com/pkg/errors"
)

func NewChargerClient(addr, username, password string) common.Client {
	return &OpenEVSEClient{
		addr:     addr,
		username: username,
		password: password,
		cli:      &http.Client{},
	}
}

func NewOpenEVSEClient(addr, username, password string) *OpenEVSEClient {
	return &OpenEVSEClient{
		addr:     addr,
		username: username,
		password: password,
		cli:      &http.Client{},
	}
}

type OpenEVSEClient struct {
	addr     string
	username string
	password string
	cli      *http.Client
}

type RapiResponse struct {
	Cmd   string `json:"cmd"`
	Ret   string `json:"ret"`
	Error string `json:"error"`
}

// CurrentCapacityInfo gives us the current capacity info
// See: https://github.com/openenergymonitor/open_evse/blob/432c05cf6e90caadad495b8d24e9321e293524c6/firmware/open_evse/rapi_proc.h#L237-L245
type CurrentCapacityInfo struct {
	// MinAmps is the minimum allowed current capacity
	MinAmps uint64
	// MaxAmps is max hardware allowed current capacity MAX_CURRENT_CAPACITY_Ln
	MaxAmps uint64
	// PilotAmps is the current capacity advertised by pilot
	PilotAmps uint64
	// CurrentMaxAmps is the max configured allowed current capacity (saved to EEPROM)
	// OpenEVSE allows changing active values of some settings, without committing them
	// to EEPROM. The active value does not show up in this API call, unless it's saved to
	// EEPROM.
	CurrentMaxAmps uint64
}

// GetStateResponse holds the info returned by the $GS command.
// See: https://github.com/openenergymonitor/open_evse/blob/432c05cf6e90caadad495b8d24e9321e293524c6/firmware/open_evse/rapi_proc.h#L289-L295
type GetStateResponse struct {
	State      uint64
	Elapsed    uint64
	PilotState uint64
	VFlags     uint64
}

func (h *OpenEVSEClient) authHeaders() string {
	creds := fmt.Sprintf("%s:%s", h.username, h.password)
	encoded := b64.StdEncoding.EncodeToString([]byte(creds))
	header := fmt.Sprintf("Basic %s", encoded)
	return header
}

func (h *OpenEVSEClient) url(cmd string) string {
	encoded := url.QueryEscape(cmd)
	return fmt.Sprintf("http://%s/r?json=1&rapi=%s", h.addr, encoded)
}

func (h *OpenEVSEClient) doGet(setting string) (RapiResponse, error) {
	req, err := http.NewRequest("GET", h.url(setting), nil)
	if err != nil {
		return RapiResponse{}, err
	}
	req.Header.Set("Authorization", h.authHeaders())

	resp, err := h.cli.Do(req)
	if err != nil {
		return RapiResponse{}, errors.Wrap(err, "sending request")
	}
	defer resp.Body.Close()

	var ret RapiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ret); err != nil {
		return RapiResponse{}, errors.Wrap(err, "unmarshaling response")
	}

	return ret, nil
}

func (h *OpenEVSEClient) validateRAPIResponse(resp RapiResponse) error {
	if resp.Error != "" {
		return fmt.Errorf("error starting evse: %q", resp.Error)
	}

	if strings.HasPrefix(resp.Ret, "$NK") {
		return fmt.Errorf("got error response from RAPI: %q", resp.Ret)
	}

	return nil
}

func (h *OpenEVSEClient) Start() error {
	response, err := h.doGet("$FE")
	if err != nil {
		return errors.Wrap(err, "starting evse")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return errors.Wrap(err, "validating response")
	}
	return nil
}

func (h *OpenEVSEClient) Stop() error {
	response, err := h.doGet("$FS")
	if err != nil {
		return errors.Wrap(err, "starting evse")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return errors.Wrap(err, "validating response")
	}
	return nil
}

func (h *OpenEVSEClient) SetAmp(amp uint64) error {
	response, err := h.doGet(fmt.Sprintf("$SC %d", amp))
	if err != nil {
		return errors.Wrap(err, "starting evse")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return errors.Wrap(err, "validating response")
	}

	if response.Ret == "" || !strings.HasPrefix(response.Ret, "$OK") {
		return fmt.Errorf("error response from RAPI: %s", response.Ret)
	}
	return nil
}

func (h *OpenEVSEClient) GetCurrentCapacityInfo() (CurrentCapacityInfo, error) {
	response, err := h.doGet("$GC")
	if err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "fetching current capacity info")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "validating response")
	}

	values := strings.Split(response.Ret, " ")
	if len(values) != 5 {
		return CurrentCapacityInfo{}, fmt.Errorf("unexpected response: %s", response.Ret)
	}

	minAmps, err := strconv.ParseUint(values[1], 10, 64)
	if err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "parsing min amps")
	}

	maxAmps, err := strconv.ParseUint(values[2], 10, 64)
	if err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "parsing max amps")
	}

	pilotAmps, err := strconv.ParseUint(values[3], 10, 64)
	if err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "parsing pilot amps")
	}

	currentMaxAmps, err := strconv.ParseUint(strings.Split(values[4], "^")[0], 10, 64)
	if err != nil {
		return CurrentCapacityInfo{}, errors.Wrap(err, "parsing currentMaxAmps amps")
	}

	return CurrentCapacityInfo{
		MinAmps:        minAmps,
		MaxAmps:        maxAmps,
		PilotAmps:      pilotAmps,
		CurrentMaxAmps: currentMaxAmps,
	}, nil
}

func (h *OpenEVSEClient) GetChargeCurrentAndVoltage() (uint64, uint64, error) {
	response, err := h.doGet("$GG")
	if err != nil {
		return 0, 0, errors.Wrap(err, "fetching charge current and voltage")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return 0, 0, errors.Wrap(err, "validating response")
	}

	values := strings.Split(response.Ret, " ")
	if len(values) != 3 {
		return 0, 0, fmt.Errorf("unexpected response: %s", response.Ret)
	}

	milliAmps, err := strconv.ParseUint(values[1], 10, 64)
	if err != nil {
		return 0, 0, errors.Wrap(err, "parsing milliAmps")
	}

	milliVolts, err := strconv.ParseUint(strings.Split(values[2], "^")[0], 10, 64)
	if err != nil {
		return 0, 0, errors.Wrap(err, "parsing milliVolts")
	}

	return milliAmps, milliVolts, nil
}

func (h *OpenEVSEClient) GetState() (GetStateResponse, error) {
	response, err := h.doGet("$GS")
	if err != nil {
		return GetStateResponse{}, errors.Wrap(err, "starting evse")
	}

	if err := h.validateRAPIResponse(response); err != nil {
		return GetStateResponse{}, errors.Wrap(err, "validating response")
	}

	values := strings.Split(response.Ret, " ")
	if len(values) != 5 {
		return GetStateResponse{}, fmt.Errorf("unexpected response: %s", response.Ret)
	}

	state, err := strconv.ParseUint(values[1], 16, 64)
	if err != nil {
		return GetStateResponse{}, errors.Wrap(err, "parsing state")
	}

	elapsed, err := strconv.ParseUint(values[2], 10, 64)
	if err != nil {
		return GetStateResponse{}, errors.Wrap(err, "parsing elapsed")
	}

	pilotState, err := strconv.ParseUint(values[3], 16, 64)
	if err != nil {
		return GetStateResponse{}, errors.Wrap(err, "parsing pilotState")
	}

	vflags, err := strconv.ParseUint(strings.Split(values[4], "^")[0], 10, 64)
	if err != nil {
		return GetStateResponse{}, errors.Wrap(err, "parsing vflags")
	}

	return GetStateResponse{
		State:      state,
		Elapsed:    elapsed,
		PilotState: pilotState,
		VFlags:     vflags,
	}, nil
}
