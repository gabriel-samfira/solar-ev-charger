package common

type BasicWorker interface {
	Start() error
	Stop() error
}

type Client interface {
	Start() error
	Stop() error
	SetAmp(newVal uint64) error
}
