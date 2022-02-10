package params

type DBusState struct {
	Consumers map[string]float64
	Producers map[string]float64
}

type ChargerState struct {
	Active            bool
	CurrentUsage      float64
	CurrentAmpSetting float64
}
