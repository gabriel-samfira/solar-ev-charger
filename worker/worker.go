package worker

import (
	"context"
	"fmt"
	"solar-ev-charger/config"
	"solar-ev-charger/eCharger/client"
	"solar-ev-charger/params"
	"sync"
	"time"

	"github.com/juju/loggo"
	"github.com/pkg/errors"
)

var log = loggo.GetLogger("sevc.worker")

func NewWorker(ctx context.Context, cfg *config.Config, dbusChanges chan params.DBusState, chargerChanges chan params.ChargerState) (*Worker, error) {
	cli := client.NewChargerClient(cfg.Charger.StationAddress)
	return &Worker{
		dbusChanges:    dbusChanges,
		chargerChanges: chargerChanges,
		closed:         make(chan struct{}),
		quit:           make(chan struct{}),
		ctx:            ctx,
		cfg:            *cfg,
		chargerClient:  cli,
	}, nil
}

type Worker struct {
	dbusChanges    chan params.DBusState
	chargerChanges chan params.ChargerState

	dbusState    params.DBusState
	chargerState params.ChargerState

	chargerClient client.Client

	cfg config.Config

	ctx    context.Context
	closed chan struct{}
	quit   chan struct{}

	mux sync.Mutex
}

func (w *Worker) syncState() error {
	var stationAmps uint64
	var desiredState bool = true
	var availableAmps uint64

	var totalConsumption float64
	var totalProduction float64
	chargerConsumption := w.chargerState.CurrentUsage

	for _, val := range w.dbusState.Consumers {
		totalConsumption += val
	}

	for _, val := range w.dbusState.Producers {
		totalProduction += val
	}

	householdConsumption := totalConsumption - chargerConsumption
	available := uint64(totalProduction - householdConsumption)
	if available <= 0 {
		stationAmps = 0
	} else {
		stationAmps = available / w.cfg.ElectricalPresure
	}

	if availableAmps < uint64(w.cfg.MinAmpThreshold) {
		stationAmps = uint64(w.cfg.MinAmpThreshold)
		if w.cfg.ToggleStationOnThreshold {
			desiredState = false
		}
	}

	if availableAmps > uint64(w.cfg.MaxAmpLimit) {
		stationAmps = uint64(w.cfg.MaxAmpLimit)
	}

	if desiredState && !w.chargerState.Active {
		if err := w.chargerClient.Start(); err != nil {
			return errors.Wrap(err, "starting charger")
		}
	}

	log.Infof("Desired state is %v, amp is %v", desiredState, stationAmps)

	if !desiredState && w.chargerState.Active {
		if err := w.chargerClient.Stop(); err != nil {
			return errors.Wrap(err, "stopping charger")
		}
	}

	if err := w.chargerClient.SetAmp(stationAmps); err != nil {
		return errors.Wrap(err, "setting station amps")
	}
	return nil
}

func (w *Worker) loop() {
	timer := time.NewTicker(time.Duration(w.cfg.BackoffThreshold) * time.Second)
	defer func() {
		timer.Stop()
		close(w.chargerChanges)
		close(w.dbusChanges)
		close(w.closed)
	}()

	for {
		select {
		case <-timer.C:
			if err := w.syncState(); err != nil {
				log.Errorf("failed to sync state: %s", err)
			}
		case change, ok := <-w.dbusChanges:
			if !ok {
				return
			}
			w.mux.Lock()
			w.dbusState = change
			w.mux.Unlock()
		case change, ok := <-w.chargerChanges:
			if !ok {
				return
			}
			w.mux.Lock()
			w.chargerState = change
			w.mux.Unlock()
		case <-w.quit:
			return
		case <-w.ctx.Done():
			return
		}
	}
}

func (w *Worker) Start() error {
	go w.loop()
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
