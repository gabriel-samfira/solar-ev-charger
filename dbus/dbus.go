package dbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	dbus "github.com/godbus/dbus/v5"
	"github.com/juju/loggo"
	"github.com/pkg/errors"

	"solar-ev-charger/config"
	"solar-ev-charger/params"
)

var log = loggo.GetLogger("sevc.dbus")

func NewDBusWorker(ctx context.Context, cfg *config.Config, stateChan chan params.DBusState) (*Worker, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, errors.Wrap(err, "creating dbus connection")
	}

	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "validating config")
	}

	state := params.DBusState{
		Consumers: map[string]float64{},
		Producers: map[string]float64{},
	}

	worker := &Worker{
		conn:         conn,
		ctx:          ctx,
		closed:       make(chan struct{}),
		quit:         make(chan struct{}),
		inputSensors: cfg.InputSensors,
		consumers:    cfg.Consumers,
		state:        state,
		backoff:      cfg.BackoffThreshold,
		stateChanged: stateChan,
	}
	return worker, nil
}

type Worker struct {
	conn   *dbus.Conn
	ctx    context.Context
	closed chan struct{}
	quit   chan struct{}

	inputSensors []config.InputSensor
	consumers    []config.Consumer

	mut   sync.Mutex
	state params.DBusState

	stateChanged chan params.DBusState

	backoff uint
}

func (w *Worker) initState() error {
	w.mut.Lock()
	defer w.mut.Unlock()

	for _, consumer := range w.consumers {
		ret, err := w.fetchValueFromDBus(consumer.Interface, consumer.Path)
		if err != nil {
			return errors.Wrap(err, "fetching value from dbus")
		}
		val, err := valueAsFloat(ret)
		if err != nil {
			return errors.Wrap(err, "converting value to float64")
		}
		w.state.Consumers[consumer.Path] = val
	}

	for _, sensor := range w.inputSensors {
		ret, err := w.fetchValueFromDBus(sensor.Interface, sensor.Path)
		if err != nil {
			return errors.Wrap(err, "fetching value from dbus")
		}
		val, err := valueAsFloat(ret)
		if err != nil {
			return errors.Wrap(err, "converting value to float64")
		}
		w.state.Producers[sensor.Path] = val * sensor.InputMultiplier
	}
	return nil
}

func (w *Worker) fetchValueFromDBus(dbusInterface, path string) (interface{}, error) {
	var ret interface{}
	obj := w.conn.Object(dbusInterface, dbus.ObjectPath(path))
	err := obj.Call("com.victronenergy.BusItem.GetValue", 0).Store(&ret)
	if err != nil {
		return ret, errors.Wrapf(err, "fetching %s from dbus", path)
	}
	log.Debugf("got %v (%T) for %s", ret, ret, path)
	return ret, nil
}

func valueAsFloat(val interface{}) (float64, error) {
	switch consumerValue := val.(type) {
	case int:
		return float64(consumerValue), nil
	case float64:
		return consumerValue, nil
	case float32:
		return float64(consumerValue), nil
	default:
		return 0, fmt.Errorf("invalid type %T", val)
	}
}

func (w *Worker) dbusLoop() {
	c := make(chan *dbus.Message, 10)
	w.conn.Eavesdrop(c)

	for {
		select {
		case msg := <-c:
			if msg.Type != dbus.TypeSignal {
				continue
			}
			w.mut.Lock()
			var changed bool
			for _, message := range msg.Body {
				var signalBody map[string]map[string]dbus.Variant
				switch val := message.(type) {
				case map[string]map[string]dbus.Variant:
					signalBody = val
				default:
					log.Warningf("got invalid type: %T", message)
					continue
				}

				for key, value := range signalBody {
					// key is the dbus path
					// value is a map[string]dbus.Variant.
					// The dbus paths we're interested in will always return
					// a map of the following form:
					//
					// map[string]dbus.Variant = {
					// 	"Text": "some description",
					// 	"Value": "the value",
					// }
					if _, ok := value["Value"]; !ok {
						continue
					}
					val := value["Value"].Value()
					for _, consumer := range w.consumers {
						if consumer.Path == key {
							consumerValue, err := valueAsFloat(val)
							if err != nil {
								log.Warningf("invalid type for %s: %T (%s)", key, val, err)
								continue
							}
							if currentValue, ok := w.state.Consumers[key]; ok && currentValue != consumerValue {
								w.state.Consumers[key] = consumerValue
								changed = true
							}
							break
						}
					}

					for _, producer := range w.inputSensors {
						if producer.Path == key {
							consumerValue, err := valueAsFloat(val)
							if err != nil {
								log.Warningf("invalid type for %s: %T (%s)", key, val, err)
								continue
							}
							newValue := consumerValue * producer.InputMultiplier
							if currentValue, ok := w.state.Producers[key]; ok && currentValue != newValue {
								w.state.Producers[key] = newValue
								changed = true
							}
							break
						}
					}
				}
			}

			if changed {
				select {
				case w.stateChanged <- w.state:
				case <-time.After(30 * time.Second):
					log.Errorf("failed to send state change after 30 seconds")
				}
			}
			w.mut.Unlock()
		case <-w.ctx.Done():
			w.conn.Close()
			close(w.closed)
			return
		case <-w.quit:
			w.conn.Close()
			close(w.closed)
			return
		}
	}
}

func (w *Worker) Start() error {
	// We need to init state before we enable monitoring on the dbus connection.
	if err := w.initState(); err != nil {
		return errors.Wrap(err, "initializing state")
	}

	var rules = []string{
		"type='signal',member='ItemsChanged',path='/',interface='com.victronenergy.BusItem'",
	}
	busObj := w.conn.BusObject()
	var flag uint = 0

	call := busObj.Call("org.freedesktop.DBus.Monitoring.BecomeMonitor", 0, rules, flag)
	if call.Err != nil {
		return errors.Wrap(call.Err, "becoming monitor")
	}

	go w.dbusLoop()
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
