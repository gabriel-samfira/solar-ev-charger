package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"solar-ev-charger/config"
	"solar-ev-charger/dbus"
	"solar-ev-charger/params"

	"github.com/juju/loggo"
)

var log = loggo.GetLogger("sevc.cmd")

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

	if err := cfg.Validate(); err != nil {
		log.Errorf("error validating config: %q", err)
		os.Exit(1)
	}

	statusUpdates := make(chan params.State, 10)

	go func() {
		for {
			select {
			case s := <-statusUpdates:
				asJs, _ := json.MarshalIndent(s, "", "  ")
				fmt.Printf("%s\n", asJs)
			case <-ctx.Done():
				return
			}
		}
	}()

	dbusWorker, err := dbus.NewDBusWorker(ctx, cfg, statusUpdates)
	if err != nil {
		log.Errorf("error creating worker: %q", err)
		os.Exit(1)
	}

	if err := dbusWorker.Start(); err != nil {
		log.Errorf("starting dbus worker: %q", err)
		os.Exit(1)
	}

	<-ctx.Done()
}
