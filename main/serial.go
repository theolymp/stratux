package main

import (
	"log"
	"strings"
	"time"

	"github.com/tarm/serial"
)



type SerialDevice interface {
	OpenSerial() bool
	GetDeviceConfig() SerialDeviceConfig
	OnConfigUpdate(cfg SerialDeviceConfig)
	InitDevice()
	Write([]byte) bool
	Shutdown()
}

// Represents a Serial device, could either be SerialIn (GPS), SerialIn (UAT Radio), SerialOut (NMEA/GDL90), ...
// Note that unlike an SDR, a serial device can be multi-purpose, e.g. NMEA-IN -> GDL90 out, ...
type SerialDeviceData struct {
	DeviceConfig SerialDeviceConfig 
	Properties map[string]string
	port       *serial.Port
}



func (dev *SerialDeviceData) GetDeviceConfig() SerialDeviceConfig {
	return dev.DeviceConfig
}

func (dev *SerialDeviceData) OpenSerial() bool {
	dev.Shutdown()
	serialConfig := &serial.Config{Name: dev.DeviceConfig.DevPath, Baud: int(dev.DeviceConfig.Baud)}
	port, err := serial.OpenPort(serialConfig)
	if err != nil {
		addSingleSystemErrorf("serial", "Failed to open serial device %s (%s baud %d): %s", dev.DeviceConfig.HumanReadableName, dev.DeviceConfig.DevPath, dev.DeviceConfig.Baud, err.Error())
		return false
	}
	log.Printf("Opened serial device %s (%s baud %d)", dev.DeviceConfig.HumanReadableName, dev.DeviceConfig.DevPath, dev.DeviceConfig.Baud)
	dev.port = port
	return true
}

func (dev *SerialDeviceData) OnConfigUpdate(cfg SerialDeviceConfig) {
	dev.Shutdown()
	SerialManager.onSerialAvailable(cfg, dev.Properties)
}

func (dev *SerialDeviceData) Write(data []byte) bool {
	if dev.port != nil {
		_, err := dev.port.Write(data)
		if err == nil {
			return true
		}
	}
	return false
}

func (dev *SerialDeviceData) Flush() {
	if dev.port != nil {
		dev.port.Flush()
	}
}

func (dev *SerialDeviceData) Shutdown() {
	if dev.port != nil {
		dev.port.Close()
		dev.port = nil
		DeviceConfigManager.onSerialDisconnected(dev)
	}
}

func (dev *SerialDeviceData) ReadForever() {
}

func (dev *SerialDeviceData) InitDevice() {
}

func (dev *SerialDeviceData) AutoBaudNmea() bool {
	baudrates := []uint32 { dev.DeviceConfig.Baud, 115200, 57600, 38400, 9600, 4800 }
	origBaud := dev.DeviceConfig.Baud
	dev.Shutdown()
	for _, baud := range baudrates {
		dev.DeviceConfig.Baud = baud
		if !dev.OpenSerial() {
			dev.DeviceConfig.Baud = origBaud
			return false
		}
		// Check if we get any data...
		time.Sleep(3 * time.Second)
		buffer := make([]byte, 10000)
		dev.port.Read(buffer)
		splitted := strings.Split(string(buffer), "\n")
		for _, line := range splitted {
			_, validNMEAcs := validateNMEAChecksum(line)
			if validNMEAcs {
				// looks a lot like NMEA.. use it
				log.Printf("Detected serial port %s with baud %d", dev.DeviceConfig.DevPath, baud)
				return true
			}
		}
	}
	dev.DeviceConfig.Baud = origBaud
	return false
}



type SerialManagerType struct {
}
var SerialManager SerialManagerType

func (s *SerialManagerType) onSdrAvailable(cfg SdrDeviceConfig) {}

// Not needed, interface function
func (s *SerialManagerType) onSerialAvailable(cfg SerialDeviceConfig, props map[string]string) {
	if cfg.CapsConfigured == 0 {
		return // unused device
	}
	var dev SerialDevice
	deviceData := &SerialDeviceData{DeviceConfig: cfg, Properties: props}
	if (cfg.CapsConfigured & CAP_NMEA_IN) > 0 {
		dev = &SerialDeviceDataGps{SerialDeviceData : *deviceData, TimeOffsetPpsMs: 100 * time.Millisecond}
	} else {
		dev = deviceData
	}
	if dev.OpenSerial() {
		DeviceConfigManager.onSerialDeviceConnected(dev)
		go dev.InitDevice()
	}
}

func (s *SerialManagerType) PublishSerial(capability uint32, data []byte) {
	serials := DeviceConfigManager.GetConnectedSerials()
	for _, serial := range serials {
		if serial.GetDeviceConfig().CapsConfigured  & capability > 0 {
			serial.Write(data)
		}
	}
}

func serialInit() {
	DeviceConfigManager.AddDeviceEventListener(&SerialManager)
}
