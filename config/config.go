package config

import (
	"fmt"
	"net"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
)

func NewConfig(cfgFile string) (*Config, error) {
	var config Config
	if _, err := toml.DecodeFile(cfgFile, &config); err != nil {
		return nil, errors.Wrap(err, "decoding toml")
	}
	if err := config.Validate(); err != nil {
		return nil, errors.Wrap(err, "validating config")
	}
	return &config, nil

}

type Config struct {
	// ElectricalPresure is the output Voltage.
	ElectricalPresure uint64 `toml:"electrical_presure"`
	// InputSensors is list of dbus services that can be used to gauge
	// power production.
	InputSensors []InputSensor `toml:"input_sensors"`
	// Consumers is a list of dbus services exposed by fornius that can
	// be used to gauge power consumption.
	Consumers []Consumer `toml:"consumers"`
	// MaxAmpLimit is the maximum aperage we can set on the EV charging
	// station.
	MaxAmpLimit uint `toml:"max_amp_limit"`
	// MinAmpCutoff is the amperage threshold where we turn off the EV
	// charging station.
	MinAmpCutoff uint `toml:"minimum_amp_cutoff"`
	// BackoffThreshold is the minimum amount of time we allow between updates
	// to the EV charging station. Updates from dbus come frequently, and
	// values can vary quite a lot based on cloud cover. We don't want to
	// change amperage to the charging station too frequently.
	BackoffThreshold uint `toml:"backoff_interval"`
	// StationAddress is the API endpoint of the charging station.
	StationAddress string `toml:"station_ip"`
}

func (c *Config) Validate() error {
	if c.Consumers == nil || len(c.Consumers) == 0 {
		return fmt.Errorf("no consumers defined")
	}

	if c.ElectricalPresure == 0 {
		return fmt.Errorf("electrical_presure needs to be non zero")
	}

	for _, consumer := range c.Consumers {
		if err := consumer.Validate(); err != nil {
			return errors.Wrap(err, "validating consumer")
		}
	}

	if c.InputSensors == nil || len(c.InputSensors) == 0 {
		return fmt.Errorf("no input sensors defined")
	}

	for _, sensor := range c.InputSensors {
		if err := sensor.Validate(); err != nil {
			return errors.Wrap(err, "validation sensor")
		}
	}

	ip := net.ParseIP(c.StationAddress)
	if ip == nil {
		return fmt.Errorf("invalid station IP address: %s", c.StationAddress)
	}

	return nil
}

type InputSensor struct {
	Interface string `toml:"dbus_interface"`
	Path      string `toml:"path"`
	// InputMultiplier is the multiplier for the value returned by the
	// InputSensor.
	// In most cases, your solar panel setup will have one sensor gauging
	// the solar output available at any moment. The output of your setup
	// however, depends on the number of solar panels installed and their
	// capacity. That means that you will need to measure the total output
	// of your system for each Volt or Watt measured by your sensor. Remember,
	// the sensor measures solar output, not your system output.
	// If your input sensor returns Volts, you will need to
	// determine the Watts per Volt your system outputs. Adjust this
	// value to get the total amount of Watts you produce at every given time.
	// If your sensor returns Watts, you need to measure the output of your
	// system for each Watt measured by your sensor and set this multiplier
	// accordingly.
	InputMultiplier float64 `toml:"input_sensor_multiplier"`
}

func (i *InputSensor) Validate() error {
	if i.InputMultiplier == 0 {
		i.InputMultiplier = 1
	}
	return nil
}

type Consumer struct {
	Interface string `toml:"dbus_interface"`
	Path      string `toml:"path"`
}

func (c *Consumer) Validate() error {
	return nil
}
