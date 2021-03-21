package main


type SerialDevice interface {
	GetDevPath() string
	GetDeviceConfig() SerialDeviceConfig
	OnConfigUpdate(cfg SerialDeviceConfig)
	Shutdown()
}

// Represents a Serial device, could either be SerialIn (GPS) or SerialOut (NMEA/GDL90)
type SerialDeviceData struct {
	DevPath    string
	Baud       uint32
	DeviceConfig SerialDeviceConfig 
}

func (dev *SerialDeviceData) GetDevPath(cfg SerialDeviceConfig) string {
	return dev.DevPath
}

func (dev *SerialDeviceData) GetDeviceConfig() SerialDeviceConfig {
	return dev.DeviceConfig
}

func (dev *SerialDeviceData) OnConfigUpdate(cfg SerialDeviceConfig) {

}

func (dev *SerialDeviceData) Shutdown() {
	
}