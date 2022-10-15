package openEVSE

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"solar-ev-charger/chargers/common"
	"solar-ev-charger/chargers/openEVSE/client"
	"solar-ev-charger/config"
	"solar-ev-charger/params"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/juju/loggo"
	"github.com/pkg/errors"
)

var log = loggo.GetLogger("sevc.OpenEVSE")

func NewWorker(ctx context.Context, cfg *config.Config, stateChan chan params.ChargerState) (common.BasicWorker, error) {
	evseCli := client.NewOpenEVSEClient(cfg.OpenEVSE.Address, cfg.OpenEVSE.Username, cfg.OpenEVSE.Password)
	return &Worker{
		stateChanged:     stateChan,
		ctx:              ctx,
		cfg:              *cfg,
		closed:           make(chan struct{}),
		quit:             make(chan struct{}),
		mqttDisconnected: make(chan struct{}),
		evseCli:          evseCli,
		mqttTopic:        fmt.Sprintf("%s/#", cfg.OpenEVSE.BaseTopic),
	}, nil
}

type chargerStatus struct {
	// currentAmpSetting is the max current set on the station ($SC)
	currentAmpSetting uint64
	// currentUsage is the current at which the car is charging
	currentUsage float64
	// enabled indicates the current state of the charger
	enabled bool
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
	evseCli          *client.OpenEVSEClient
	mqttDisconnected chan struct{}
	mqttTopic        string
}

func (w *Worker) mqttOnConnect(client mqtt.Client) {
	log.Infof("Connected to %s", w.cfg.OpenEVSE.MQTT.Broker)
}

func (w *Worker) mqttConnectionLostHandler(client mqtt.Client, err error) {
	log.Infof("Connection to %s has been lost: %q", w.cfg.OpenEVSE.MQTT.Broker, err)
	select {
	case <-w.mqttDisconnected:
	default:
		close(w.mqttDisconnected)
	}
}

func (w *Worker) sendLocalState() error {
	state := params.ChargerState{
		Active:            w.status.enabled,
		CurrentUsage:      w.status.currentUsage,
		CurrentAmpSetting: float64(w.status.currentAmpSetting),
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

	switch topic {
	case "amp":
		val, err := strconv.ParseFloat(string(payload), 64)
		if err != nil {
			log.Errorf("failed to parse payload: %s", string(payload))
			return
		}
		w.status.currentUsage = val
	case "state":
		val, err := strconv.ParseUint(string(payload), 10, 64)
		if err != nil {
			log.Errorf("failed to parse payload: %s", string(payload))
			return
		}
		w.status.enabled = val == 1
	default:
		return
	}
}

func (w *Worker) connectMQTT() (mqtt.Client, error) {
	if err := w.initState(); err != nil {
		return nil, errors.Wrap(err, "initializing state")
	}
	opts, err := w.cfg.OpenEVSE.MQTT.ClientOptions()
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
	timer := time.NewTicker(5 * time.Second)

	defer func() {
		timer.Stop()
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
		case <-timer.C:
			w.mux.Lock()

			currentState, err := w.evseCli.GetCurrentCapacityInfo()
			if err != nil {
				log.Errorf("failed to get current state from RAPI: %+v", err)
				continue
			}
			w.status.currentAmpSetting = currentState.CurrentMaxAmps

			if err := w.sendLocalState(); err != nil {
				log.Errorf("failed to send state: %q", err)
			}
			w.mux.Unlock()
		case <-w.ctx.Done():
			return
		case <-w.quit:
			return
		case <-w.mqttDisconnected:
			w.client = nil
		}
	}
}

func (w *Worker) fetchStatusFromAPI() (chargerStatus, error) {
	milliAmps, _, err := w.evseCli.GetChargeCurrentAndVoltage()
	if err != nil {
		return chargerStatus{}, errors.Wrap(err, "getting charge current and voltage")
	}

	currentCapacity, err := w.evseCli.GetCurrentCapacityInfo()
	if err != nil {
		return chargerStatus{}, errors.Wrap(err, "getting current capacity info")
	}

	state, err := w.evseCli.GetState()
	if err != nil {
		return chargerStatus{}, errors.Wrap(err, "getting state")
	}

	var usage float64
	if milliAmps > 0 {
		usage = float64(milliAmps) / 1000
	}

	return chargerStatus{
		currentUsage:      usage,
		currentAmpSetting: currentCapacity.CurrentMaxAmps,
		enabled:           state.State == 1,
	}, nil
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
	return nil
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

func (w *Worker) Start() error {
	if w.cfg.OpenEVSE.UseMQTT {
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
