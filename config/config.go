package config

import (
	"fmt"
	"net"

	"github.com/BurntSushi/toml"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pkg/errors"
)

type LogLevel string

const (
	ClientID          = "solar-ev-charger"
	Trace    LogLevel = "trace"
	Debug    LogLevel = "debug"
	Info     LogLevel = "info"
	Warning  LogLevel = "warning"
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
	// MinAmpThreshold is the minimum amps we will set on the station.
	MinAmpThreshold uint `toml:"minimum_amp_threshold"`
	// DisableChargingThreshold is the amp threshold below which we turn off the
	// charging station.
	DisableChargingThreshold uint `toml:"disable_charging_threshold"`
	// EnableChargingThreshold is the amp threshold above which we turn on the
	// charging station.
	EnableChargingThreshold uint `toml:"enable_charging_threshold"`
	// ToggleStationOnThreshold will turn the station on or off. The station will
	// be turned off when the available amperage dips bellow
	// disable_charging_threshold and will be turned back on when available amps
	// go above enable_charging_threshold.
	ToggleStationOnThreshold bool `toml:"toggle_station_on_threshold"`
	// BackoffThreshold is the minimum amount of time we allow between updates
	// to the EV charging station. Updates from dbus come frequently, and
	// values can vary quite a lot based on cloud cover. We don't want to
	// change amperage to the charging station too frequently.
	BackoffThreshold uint `toml:"backoff_interval"`

	// LogFile is the path to the log on disk
	LogFile string `toml:"log_file"`

	// ConfiguredCharger is the charger type we want to automate.
	ConfiguredCharger string `toml:"configured_charger"`

	// Charger holds the config for the charger
	Charger Charger `toml:"eCharger"`

	// OpenEVSE holds config options for OpenEVSE
	OpenEVSE OpenEVSECharger `toml:"OpenEVSE"`

	// LogLevel sets the logging output to desired level.
	LogLevel LogLevel `toml:"log_level"`
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

	if err := c.Charger.Validate(); err != nil {
		return errors.Wrap(err, "validating charger")
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

type OpenEVSECharger struct {
	Address  string `toml:"address"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	// BaseTopic is the topic configured in OpenEVSE for MQTT.
	BaseTopic string `toml:"base_topic"`
	// MQTT represents the MQTT settings used by the station. We need
	// to use the same settings in this worker to be able to communicate
	// with the station via MQTT.
	MQTT    MQTTSettings `toml:"mqtt"`
	UseMQTT bool         `toml:"use_mqtt"`
}

func (o *OpenEVSECharger) Validate() error {
	ip := net.ParseIP(o.Address)
	if ip == nil {
		return fmt.Errorf("invalid station IP address: %s", o.Address)
	}

	if o.Username == "" || o.Password == "" {
		return fmt.Errorf("missing required username and password")
	}

	if o.UseMQTT {
		if err := o.MQTT.Validate(); err != nil {
			return errors.Wrap(err, "validating mqtt settings")
		}
	}
	return nil
}

type Charger struct {
	// StationAddress is the API endpoint of the charging station.
	StationAddress string `toml:"station_ip"`
	// MQTT represents the MQTT settings used by the station. We need
	// to use the same settings in this worker to be able to communicate
	// with the station via MQTT.
	MQTT    MQTTSettings `toml:"mqtt"`
	UseMQTT bool         `toml:"use_mqtt"`
}

func (c *Charger) Validate() error {
	ip := net.ParseIP(c.StationAddress)
	if ip == nil {
		return fmt.Errorf("invalid station IP address: %s", c.StationAddress)
	}

	if c.UseMQTT {
		if err := c.MQTT.Validate(); err != nil {
			return errors.Wrap(err, "validating mqtt settings")
		}
	}
	return nil
}

type MQTTSettings struct {
	Broker   string `toml:"broker"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

func (m *MQTTSettings) BrokerURI() (string, error) {
	if err := m.Validate(); err != nil {
		return "", errors.Wrap(err, "fetching broker URI")
	}

	uri := fmt.Sprintf("tcp://%s:%d", m.Broker, m.Port)
	return uri, nil
}

func (m *MQTTSettings) ClientOptions() (*mqtt.ClientOptions, error) {
	brokerURI, err := m.BrokerURI()
	if err != nil {
		return nil, errors.Wrap(err, "creating mqtt options")
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURI)
	if m.Username != "" {
		opts.SetUsername(m.Username)
	}
	if m.Password != "" {
		opts.SetPassword(m.Password)
	}
	opts.SetClientID(ClientID)
	return opts, nil
}

func (m *MQTTSettings) Validate() error {
	if m.Broker == "" {
		return fmt.Errorf("broker cannot be empty when mqtt is used")
	}

	if m.Port == 0 {
		m.Port = 1883
	}
	return nil
}
