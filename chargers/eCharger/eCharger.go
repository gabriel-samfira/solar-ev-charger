package eCharger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"solar-ev-charger/chargers/common"
	"solar-ev-charger/config"
	"solar-ev-charger/params"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/juju/loggo"
	"github.com/pkg/errors"
)

var log = loggo.GetLogger("sevc.eCharger")

func NewWorker(ctx context.Context, cfg *config.Config, stateChan chan params.ChargerState) (common.BasicWorker, error) {
	return &Worker{
		stateChanged:     stateChan,
		ctx:              ctx,
		cfg:              *cfg,
		closed:           make(chan struct{}),
		quit:             make(chan struct{}),
		mqttDisconnected: make(chan struct{}),
	}, nil
}

type chargerStatus struct {
	SensorData    [16]int `json:"nrg"`
	SerialNumber  string  `json:"sse"`
	Amp           int     `json:"amp,string"`
	AllowCharging int     `json:"alw,string"`
	MQTTEnabled   int     `json:"mce"`
	MQTTServer    string  `json:"mcs"`
	MQTTPort      int     `json:"mcp"`
	MQTTUsername  string  `json:"mcu"`
	MQTTKey       string  `json:"mck"`
	MQTTConnected int     `json:"mcc"`
}

type Worker struct {
	ctx    context.Context
	closed chan struct{}
	quit   chan struct{}

	stateChanged     chan params.ChargerState
	status           chargerStatus
	stateInitialized bool
	mux              sync.Mutex

	cfg config.Config

	client           mqtt.Client
	mqttDisconnected chan struct{}
	mqttTopic        string
}

func (w *Worker) mqttOnConnect(client mqtt.Client) {
	log.Infof("Connected to %s", w.cfg.Charger.MQTT.Broker)
}

func (w *Worker) mqttConnectionLostHandler(client mqtt.Client, err error) {
	log.Infof("Connection to %s has been lost: %q", w.cfg.Charger.MQTT.Broker, err)
	select {
	case <-w.mqttDisconnected:
	default:
		close(w.mqttDisconnected)
	}
}

func (w *Worker) sendLocalState() error {
	// Divide by 10. See "nrg" table: https://github.com/goecharger/go-eCharger-API-v1/blob/master/go-eCharger%20API%20v1%20EN.md
	totalUsage := w.status.SensorData[4] + w.status.SensorData[5] + w.status.SensorData[6]
	if totalUsage > 0 {
		totalUsage = totalUsage / 10
	} else {
		totalUsage = 0
	}
	currentUsage := float64(uint64(totalUsage) * w.cfg.ElectricalPresure)
	state := params.ChargerState{
		Active:            w.status.AllowCharging == 1,
		CurrentUsage:      currentUsage,
		CurrentAmpSetting: float64(w.status.Amp),
	}
	select {
	case w.stateChanged <- state:
	case <-time.After(30 * time.Second):
		return fmt.Errorf("sending state timed out after 30 seconds")
	}
	return nil
}

func (w *Worker) mqttNewMessageHandler(client mqtt.Client, msg mqtt.Message) {
	w.mux.Lock()
	defer w.mux.Unlock()

	payload := msg.Payload()
	topic := msg.Topic()

	if topic != w.mqttTopic {
		log.Debugf("got new message on topic %v; configured topic is %v", topic, w.mqttTopic)
		return
	}

	//log.Tracef("got new status update: %v", payload)
	var x chargerStatus
	if err := json.Unmarshal(payload, &x); err != nil {
		log.Errorf("failed to decode status: %q", err)
		return
	}
	// update internal state
	w.status = x

	if err := w.sendLocalState(); err != nil {
		log.Errorf("failed to send state: %q", err)
	}
}

func (w *Worker) connectMQTT() (mqtt.Client, error) {
	if err := w.initState(); err != nil {
		return nil, errors.Wrap(err, "initializing state")
	}
	opts, err := w.cfg.Charger.MQTT.ClientOptions()
	if err != nil {
		return nil, errors.Wrap(err, "fetching client options")
	}
	opts.OnConnect = w.mqttOnConnect
	opts.OnConnectionLost = w.mqttConnectionLostHandler
	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		return nil, token.Error()
	}
	log.Infof("subscribing to %s", w.mqttTopic)
	token = client.Subscribe(w.mqttTopic, 1, w.mqttNewMessageHandler)
	token.Wait()
	if token.Error() != nil {
		return nil, errors.Wrap(err, "subscribing to topic")
	}
	return client, nil
}

func (w *Worker) loopMQTT() {
	defer func() {
		w.client.Disconnect(1000)
		close(w.closed)
	}()
	for {
		if w.client == nil {
			client, err := w.connectMQTT()
			if err != nil {
				log.Errorf("failed to connect to mqtt: %q", err)
				time.Sleep(5 * time.Second)
				continue
			}
			w.client = client
			w.mqttDisconnected = make(chan struct{})
		}

		select {
		case <-w.ctx.Done():
			return
		case <-w.quit:
			return
		case <-w.mqttDisconnected:
			w.client = nil
		}
	}
}

func (w *Worker) loopHTTP() {
	timer := time.NewTicker(5 * time.Second)

	defer func() {
		timer.Stop()
		close(w.closed)
	}()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.quit:
			return
		case <-timer.C:
			w.mux.Lock()
			state, err := w.fetchStatusFromAPI()
			if err != nil {
				log.Errorf("failed to fetch status: %q", err)
				w.mux.Unlock()
				continue
			}
			w.status = state
			w.mux.Unlock()

			if err := w.sendLocalState(); err != nil {
				log.Errorf("failed to send state:%q", err)
			}
		}
	}
}

func (w *Worker) fetchStatusFromAPI() (chargerStatus, error) {
	stationAPI := fmt.Sprintf("http://%s/status", w.cfg.Charger.StationAddress)
	resp, err := http.Get(stationAPI)
	if err != nil {
		return chargerStatus{}, errors.Wrap(err, "fetching status")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return chargerStatus{}, errors.Wrap(err, "reading response")
	}
	var status chargerStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return status, errors.Wrap(err, "decoding status")
	}
	return status, nil
}

func (w *Worker) initState() error {
	w.mux.Lock()
	defer w.mux.Unlock()

	if w.stateInitialized {
		return nil
	}

	status, err := w.fetchStatusFromAPI()
	if err != nil {
		return errors.Wrap(err, "initializing state")
	}
	w.status = status
	w.stateInitialized = true
	w.mqttTopic = fmt.Sprintf("go-eCharger/%s/status", w.status.SerialNumber)
	return nil
}

func (w *Worker) Start() error {
	if w.cfg.Charger.UseMQTT {
		go w.loopMQTT()
	} else {
		go w.loopHTTP()
	}
	return nil
}

func (w *Worker) Stop() error {
	close(w.quit)
	select {
	case <-w.closed:
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timeout waiting for worker to exit")
	}
}
