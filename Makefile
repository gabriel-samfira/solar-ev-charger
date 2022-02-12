SHELL := bash

.PHONY : build-static

all:
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o solar-ev-charger ./cmd/solar-ev-charger

