package worker

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/juju/loggo"
	"github.com/pkg/errors"

	"solar-ev-charger/chargers/common"
	"solar-ev-charger/chargers/eCharger/client"
	"solar-ev-charger/config"
	"solar-ev-charger/params"
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

	chargerStateReceived bool
	dbusStateReceived    bool

	chargerClient common.Client

	cfg config.Config

	ctx    context.Context
	closed chan struct{}
	quit   chan struct{}

	mux sync.Mutex
}

func (w *Worker) syncState() error {
	if !w.chargerStateReceived || !w.dbusStateReceived {
		log.Infof("Empty charger or dbus state. Waiting for metrics.")
		return nil
	}
	var stationAmps uint64
	var availableAmps uint64
	// initialize desired state with current state.
	var desiredState bool = w.chargerState.Active

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
	// available watts after we substract household usage. We round that down.
	available := math.Floor(totalProduction - householdConsumption)
	log.Debugf("charger usage: %.2f, total usage: %.2f, production: %.2f, household: %.2f, available: %.2f", chargerConsumption, totalConsumption, totalProduction, householdConsumption, available)

	if available <= 0 {
		// We're consuming more than we're producing
		availableAmps = 0
	} else {
		// We have some excess. Convert to amps.
		availableAmps = uint64(available) / w.cfg.ElectricalPresure
	}

	var curAmpSetting uint64
	if w.chargerState.CurrentAmpSetting >= 0 {
		curAmpSetting = uint64(w.chargerState.CurrentAmpSetting)
	}

	if availableAmps > uint64(w.cfg.MaxAmpLimit) {
		// We have more power than we can set on the station. Cap it to configured maximum.
		availableAmps = uint64(w.cfg.MaxAmpLimit)
	}

	// The current state of the station is not modified if the current available amps
	// stays within the usage range defined by the disable and the enable thresholds.
	// it is a buffer zone to prevent station flapping.
	if availableAmps <= uint64(w.cfg.DisableChargingThreshold) {
		// if we dip bellow the disable threshold, we turn off the station.
		// Above this threshold we leave it on, even if we drain the batteries
		// a bit.
		desiredState = false
	} else if availableAmps >= uint64(w.cfg.EnableChargingThreshold) {
		// if the station is off and the available amps are above the enable threshold
		// we turn it back on.
		desiredState = true
	}

	stationAmps = availableAmps
	if stationAmps < uint64(w.cfg.MinAmpThreshold) {
		// We're producing less than the minimum we want to set on the station.
		stationAmps = uint64(w.cfg.MinAmpThreshold)
	}

	log.Tracef("Desired state is %v, available amps is %v (%v), station amps is %v, disable threshold %v, enable_threshold: %v ", desiredState, availableAmps, available, stationAmps, w.cfg.DisableChargingThreshold, w.cfg.EnableChargingThreshold)

	if desiredState && !w.chargerState.Active {
		log.Debugf("desired state is %v, current state is %v", desiredState, w.chargerState.Active)
		if w.cfg.ToggleStationOnThreshold {
			log.Infof("enabling charging station; available amps: %v", availableAmps)
			if err := w.chargerClient.Start(); err != nil {
				return errors.Wrap(err, "starting charger")
			}
		}
	}

	if !desiredState && w.chargerState.Active {
		log.Debugf("desired state is %v, current state is %v", desiredState, w.chargerState.Active)
		if w.cfg.ToggleStationOnThreshold {
			log.Infof("disabling charging station; available amps: %v", availableAmps)
			if err := w.chargerClient.Stop(); err != nil {
				return errors.Wrap(err, "stopping charger")
			}
		}
	}

	if curAmpSetting != stationAmps {
		log.Infof("setting station amp to %d. Previous setting was %d", stationAmps, curAmpSetting)
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
			w.dbusStateReceived = true
			w.dbusState = change
			w.mux.Unlock()
		case change, ok := <-w.chargerChanges:
			if !ok {
				return
			}
			w.mux.Lock()
			w.chargerStateReceived = true
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
