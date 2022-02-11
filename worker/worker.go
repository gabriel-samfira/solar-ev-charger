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
	var desiredState bool

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
	// available watts after we substract household usage
	available := uint64(totalProduction - householdConsumption)
	if available <= 0 {
		// We're consuming more than we're producing
		stationAmps = 0
	} else {
		// We have some excess. Convert to amps.
		stationAmps = available / w.cfg.ElectricalPresure
	}

	if stationAmps < uint64(w.cfg.MinAmpThreshold) {
		// We're producing less than the minimum we want to set on the station.
		stationAmps = uint64(w.cfg.MinAmpThreshold)
	}

	var curAmpSetting uint64
	if w.chargerState.CurrentAmpSetting >= 0 {
		curAmpSetting = uint64(w.chargerState.CurrentAmpSetting)
	}

	if stationAmps > uint64(w.cfg.MaxAmpLimit) {
		// We have more power than we can set on the station. Cap it to configured maximum.
		stationAmps = uint64(w.cfg.MaxAmpLimit)
	}

	if stationAmps <= uint64(w.cfg.DisableChargingThreshold) {
		desiredState = false
	} else if stationAmps >= uint64(w.cfg.EnableChargingThreshold) {
		desiredState = true
	}

	log.Debugf("Desired state is %v, amp is %v", desiredState, stationAmps)

	if desiredState && !w.chargerState.Active {
		log.Debugf("desired state is %v, current state is %v", desiredState, w.chargerState.Active)
		if w.cfg.ToggleStationOnThreshold {
			log.Infof("enabling charging station; available amps: %v", stationAmps)
			if err := w.chargerClient.Start(); err != nil {
				return errors.Wrap(err, "starting charger")
			}
		}
	}

	if !desiredState && w.chargerState.Active {
		log.Debugf("desired state is %v, current state is %v", desiredState, w.chargerState.Active)
		if w.cfg.ToggleStationOnThreshold {
			log.Infof("disabling charging station; available amps: %v", stationAmps)
			if err := w.chargerClient.Stop(); err != nil {
				return errors.Wrap(err, "stopping charger")
			}
		}
	}

	if curAmpSetting != stationAmps {
		log.Debugf("current amp setting (%d) differs from desired state (%d)", curAmpSetting, stationAmps)
		if err := w.chargerClient.SetAmp(stationAmps); err != nil {
			return errors.Wrap(err, "setting station amps")
		}
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
