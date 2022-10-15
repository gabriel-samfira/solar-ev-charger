package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	"github.com/juju/loggo"

	"solar-ev-charger/chargers/common"
	"solar-ev-charger/chargers/eCharger"
	"solar-ev-charger/chargers/openEVSE"
	"solar-ev-charger/config"
	"solar-ev-charger/dbus"
	"solar-ev-charger/params"
	"solar-ev-charger/util"
	"solar-ev-charger/worker"
)

var log = loggo.GetLogger("")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfgFile := flag.String("config", "", "solar-ev-charger config file")
	flag.Parse()

	if *cfgFile == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	cfg, err := config.NewConfig(*cfgFile)
	if err != nil {
		log.Errorf("error parsing config: %q", err)
		os.Exit(1)
	}

	logWriter, err := util.GetLoggingWriter(cfg)
	if err != nil {
		log.Errorf("fetching log writer: %q", err)
		os.Exit(1)
	}
	simpleWriter := loggo.NewSimpleWriter(logWriter, loggo.DefaultFormatter)
	loggo.ReplaceDefaultWriter(simpleWriter)

	switch cfg.LogLevel {
	case config.Trace:
		log.SetLogLevel(loggo.TRACE)
	case config.Debug:
		log.SetLogLevel(loggo.DEBUG)
	case config.Info:
		log.SetLogLevel(loggo.INFO)
	case config.Warning:
		log.SetLogLevel(loggo.WARNING)
	default:
		log.SetLogLevel(loggo.INFO)
	}

	if err := cfg.Validate(); err != nil {
		log.Errorf("error validating config: %q", err)
		os.Exit(1)
	}

	statusUpdates := make(chan params.DBusState, 10)
	chargerStatus := make(chan params.ChargerState, 10)
	// go func() {
	// 	for {
	// 		select {
	// 		case s := <-statusUpdates:
	// 			asJs, _ := json.MarshalIndent(s, "", "  ")
	// 			log.Infof("%s", asJs)
	// 		case c := <-chargerStatus:
	// 			asJs, _ := json.MarshalIndent(c, "", "  ")
	// 			log.Infof("%s", asJs)
	// 		case <-ctx.Done():
	// 			return
	// 		}
	// 	}
	// }()

	dbusWorker, err := dbus.NewDBusWorker(ctx, cfg, statusUpdates)
	if err != nil {
		log.Errorf("error creating worker: %q", err)
		os.Exit(1)
	}

	if err := dbusWorker.Start(); err != nil {
		log.Errorf("starting dbus worker: %+v", err)
		os.Exit(1)
	}

	var chargerWorker common.BasicWorker
	switch cfg.ConfiguredCharger {
	case "OpenEVSE":
		chargerWorker, err = openEVSE.NewWorker(ctx, cfg, chargerStatus)
	case "eCharger":
		chargerWorker, err = eCharger.NewWorker(ctx, cfg, chargerStatus)
	default:
		log.Errorf("invalid charger type: %s", cfg.ConfiguredCharger)
		os.Exit(1)
	}
	if err != nil {
		log.Errorf("error creating charger worker: %q", err)
		os.Exit(1)
	}

	if err := chargerWorker.Start(); err != nil {
		log.Errorf("starting charger worker: %q", err)
		os.Exit(1)
	}

	stateWorker, err := worker.NewWorker(ctx, cfg, statusUpdates, chargerStatus)
	if err != nil {
		log.Errorf("error creating state worker: %q", err)
		os.Exit(1)
	}

	if err := stateWorker.Start(); err != nil {
		log.Errorf("starting state worker: %q", err)
		os.Exit(1)
	}

	<-ctx.Done()
}
