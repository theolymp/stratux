package main

import(
	"context"
	"log"
	"sync"
	"strconv"
	"regexp"
	"strings"
	udev "github.com/jochenvg/go-udev"
)

const (
	CAP_1090ES = 1
	CAP_UAT = 2
	CAP_OGN = 4
	CAP_NMEA_IN = 8
	CAP_UBX_OUT = 16 // for configuring Ublox USB devices
	CAP_NMEA_OUT = 32
	CAP_GDL90_OUT = 64
)

var DeviceConfigManager DeviceConfigManagerStruct

// Config as stored in settings
type SdrDeviceConfig struct {
	Identifier string
	HumanReadableName string
	Serial string
	Capabilities uint32
	CapsConfigured uint32
	PPM int
	DevPath string
}

// Config as stored in settings
type SerialDeviceConfig struct {
	Identifier string
	HumanReadableName string
	Baud uint32
	Capabilities uint32
	CapsConfigured uint32
}


// Holds the raw configuration information for devices, will be serialized to global settings.
// The map keys are unique identifiers (e.g. SDR serial)
type DeviceConfig struct {
	Sdrs map[string]SdrDeviceConfig
	Serials map[string]SerialDeviceConfig
}

type DeviceEventListener interface {
	onSdrAvailable(cfg SdrDeviceConfig)
	onSerialAvailable(cfg SerialDeviceConfig)
}

// Holds the runtime state of each connected device and its configuration.
// Will read/write directly from/to globalSettings.Devices
type DeviceConfigManagerStruct struct {
	mu sync.Mutex
	Sdrs map[string]SdrDevice
	Serials map[string]SerialDevice

	listeners map[DeviceEventListener]bool
}

// Helper stuff
func reCompile(s string) *regexp.Regexp {
	// note , compile returns a nil pointer on error
	r, _ := regexp.Compile(s)
	return r
}

type regexUAT regexp.Regexp
type regexES regexp.Regexp
type regexOGN regexp.Regexp

var rUAT = (*regexUAT)(reCompile("str?a?t?u?x:978"))
var rES = (*regexES)(reCompile("str?a?t?u?x:1090"))
var rOGN = (*regexES)(reCompile("str?a?t?u?x:868"))

func (r *regexUAT) hasID(serial string) bool {
	if r == nil {
			return strings.HasPrefix(serial, "stratux:978")
	}
	return (*regexp.Regexp)(r).MatchString(serial)
}

func (r *regexES) hasID(serial string) bool {
	if r == nil {
			return strings.HasPrefix(serial, "stratux:1090")
	}
	return (*regexp.Regexp)(r).MatchString(serial)
}

func (r *regexOGN) hasID(serial string) bool {
	if r == nil {
			return strings.HasPrefix(serial, "stratux:868")
	}
	return (*regexp.Regexp)(r).MatchString(serial)
}


func getPPM(serial string) int {
	r, err := regexp.Compile("str?a?t?u?x:\\d+:?(-?\\d*)")
	if err != nil {
		return 0
	}

	arr := r.FindStringSubmatch(serial)
	if arr == nil {
		return 0
	}

	ppm, err := strconv.Atoi(arr[1])
	if err != nil {
		return 0
	}

	return ppm
}

func (manager* DeviceConfigManagerStruct) AddDeviceEventListener(listener DeviceEventListener) {
	manager.listeners[listener] = true
}


func (manager* DeviceConfigManagerStruct) RemoveDeviceEventListener(listener DeviceEventListener) {
	delete(manager.listeners, listener)
}

func (manager* DeviceConfigManagerStruct) notifySdrAvailable(cfg *SdrDeviceConfig) {
	for l, _ := range manager.listeners {
		l.onSdrAvailable(*cfg)
	}
}

func (manager* DeviceConfigManagerStruct) notifySerialAvailable(cfg *SerialDeviceConfig) {
	for l, _ := range manager.listeners {
		l.onSerialAvailable(*cfg)
	}
}

func (manager* DeviceConfigManagerStruct) UpdateSdrSettings(cfg SdrDeviceConfig) {
	device, isOpen := manager.Sdrs[cfg.Identifier]
	globalSettings.Devices.Sdrs[cfg.Identifier] = cfg
	if isOpen {
		// Currently open. Need to notify that config has changed
		device.OnConfigUpdate(cfg)
	}
}

func (manager* DeviceConfigManagerStruct) UpdateSerialSettings(cfg SerialDeviceConfig) {
	device, isOpen := manager.Serials[cfg.Identifier]
	globalSettings.Devices.Serials[cfg.Identifier] = cfg
	if isOpen {
		device.OnConfigUpdate(cfg)
	}
}

// Check if there is any serial device active that has the given capability and is configured to use it
func (manager* DeviceConfigManagerStruct) HasEnabledSdr(capability uint32) bool {
	for _, sdr := range manager.Sdrs {
		if (sdr.GetDeviceConfig().CapsConfigured & capability) > 0 {
			return true
		}
	}
	return false
}

// Check if there is any serial device active that has the given capability and is configured to use it
func (manager* DeviceConfigManagerStruct) HasEnabledSerial(capability uint32) bool {
	for _, serial := range manager.Serials {
		if (serial.GetDeviceConfig().CapsConfigured & capability) > 0 {
			return true
		}
	}
	return false
}

// Device was physically connected to the PI
func (manager* DeviceConfigManagerStruct) onDeviceAvailable(dev *udev.Device) {
	if cfg := manager.checkSdrDongle(dev); cfg != nil {
		log.Printf("SDR connected: " + cfg.HumanReadableName)
		manager.notifySdrAvailable(cfg)
	} else if cfg := manager.checkSerialDongle(dev); cfg != nil {
		log.Printf("Serial device connected: " + cfg.HumanReadableName)
		manager.notifySerialAvailable(cfg)
	}
}

func (manager *DeviceConfigManagerStruct) Shutdown() {
	for _, dev := range manager.Sdrs {
		dev.Shutdown()
	}
	for _, dev := range manager.Serials {
		dev.Shutdown()
	}
}

// Device was logically connected and is now in use
func (manager *DeviceConfigManagerStruct) onSdrDeviceConnected(dev SdrDevice) {
	manager.Sdrs[dev.GetDeviceConfig().Identifier] = dev
}
func (manager *DeviceConfigManagerStruct) onSerialDeviceConnected(dev SerialDevice) {
	manager.Serials[dev.GetDeviceConfig().Identifier] = dev
}

// Device was logically disconnected and is now not in use any more
func (manager *DeviceConfigManagerStruct) onSdrDisconnected(dev SdrDevice) {
	delete(manager.Sdrs, dev.GetDeviceConfig().Identifier)
}
func (manager *DeviceConfigManagerStruct) onSerialDisconnected(dev SerialDevice) {
	delete(manager.Serials, dev.GetDeviceConfig().Identifier)
}

// Device was physically disconnected from the PI
func (manager* DeviceConfigManagerStruct) onDeviceUnavailable(dev *udev.Device) {
	// During disconnect event, we can't get all the nice details, but if the device is open (and only then we care), we get the dev path at least,
	// so we need to look for that.
	path, ok := dev.Properties()["DEVPATH"]
	if ok {
		for _, dev := range manager.Sdrs {
			if dev.GetDevPath() == path {
				log.Printf("SDR disconnected: " + dev.GetDeviceConfig().HumanReadableName)
				dev.Shutdown()
				delete(manager.Sdrs, dev.GetDeviceConfig().Identifier)
				break
			}
		}
		for _, dev := range manager.Serials {
			if dev.GetDevPath() == path {
				log.Printf("Serial disconnected: " + dev.GetDeviceConfig().HumanReadableName)
				dev.Shutdown()
				delete(manager.Serials, dev.GetDeviceConfig().Identifier)
				break
			}
		}
	}
}


// Should be called at startup to scan all devices and connect to what we want
func (manager* DeviceConfigManagerStruct) DetectConnectedDevices() {
	u := udev.Udev{}
	enumerate := u.NewEnumerate()
	devices, _ := enumerate.Devices()

	for i := 0; i < len(devices); i++ {
		dev := devices[i]
		props := dev.Properties()

		_, hasVendorId := props["ID_VENDOR_ID"]
		_, hasModelId := props["ID_MODEL_ID"]
		devName, hasDevName := props["DEVNAME"]
		if hasVendorId && hasModelId || (hasDevName && devName == "/dev/ttyAMA0") {
			manager.onDeviceAvailable(dev)
		}
	}
}

// Will block and monitor for device changes (plug in/out events)
func (manager* DeviceConfigManagerStruct) MonitorDevices() {
	u := udev.Udev{}
	m := u.NewMonitorFromNetlink("udev")
	ctx, _ := context.WithCancel(context.Background())
	ch, _ := m.DeviceChan(ctx)
	for device := range ch {
		action := device.Action()
		if action == "add" {
			manager.onDeviceAvailable(device)
		}
		if action == "remove" {
			manager.onDeviceUnavailable(device)
		}
	}
}

// We support a myriad of different serial devices.. namely
// - Ublox GPS Devices as type CAP_NMEA_IN | CAP_UBX_OUT
// - TTGO T-Beam as type CAP_NMEA_IN (there is detection in place later on that will detect OGNTRACKER firmware and configure it)
// - TTGO T-Motion (SoftRF)
// - Stratux UAT Radio
// - Generic USB Serial Dongles
// - RPI builtin uart pins
// This function will use udev to try to detect all these correctly and create the respective devices
func (manager* DeviceConfigManagerStruct) checkSerialDongle(dev *udev.Device) *SerialDeviceConfig {
	props := dev.Properties()

	genericCapabilities := uint32(CAP_NMEA_IN | CAP_NMEA_OUT | CAP_UBX_OUT | CAP_GDL90_OUT)

	if props["DEVNAME"] == "/dev/ttyAMA0" {
		// RPI Builtin serial port. Used as GPS by default
		serial := "RPi UART ttyAMA0"
		if cfg, ok := globalSettings.Devices.Serials[serial]; ok {
			return &cfg
		}
		return &SerialDeviceConfig {
			HumanReadableName: serial,
			Baud: 38400,
			Capabilities: genericCapabilities,
			CapsConfigured: CAP_NMEA_IN,
		}
	} else if props["SUBSYSTEM"] == "tty" && props["ID_BUS"] == "usb" {
		// Other serial devices
		serial := props["ID_SERIAL"]
		if cfg, ok := globalSettings.Devices.Serials[serial]; ok {
			return &cfg
		}

		vendor, _ := props["ID_VENDOR_ID"]
		model, _ := props["ID_MODEL_ID"]
		modelName, _ := props["ID_MODEL"]

		// Normal USB/Serial device. Config detection is quite complex and depends on many attributes..
		if vendor == "1546" { // UBlox GPS
			return &SerialDeviceConfig {
				HumanReadableName: strings.ReplaceAll(serial, "_", " "),
				Baud: 9600, // ublox default baud. GPS Code will run autodetection anyway
				Capabilities: CAP_NMEA_IN | CAP_UBX_OUT,
				CapsConfigured: CAP_NMEA_IN | CAP_UBX_OUT,
			}
		} else if vendor == "067b" && model == "2303" { // Prolific GPS
			return &SerialDeviceConfig {
				HumanReadableName: strings.ReplaceAll(serial, "_", " "),
				Baud: 4800,
				Capabilities: CAP_NMEA_IN,
				CapsConfigured: CAP_NMEA_IN,
			}
		} else if vendor == "0403" && model == "7028" { // Stratux UAT Radio
			return &SerialDeviceConfig {
				HumanReadableName: strings.ReplaceAll(serial, "_", " "),
				Baud: 2000000,
				Capabilities: CAP_UAT,
				CapsConfigured: CAP_UAT,
			}
		} else {
			cfg := &SerialDeviceConfig {
				HumanReadableName: strings.ReplaceAll(modelName, "_", " "),
				Baud: 9600,
				Capabilities: genericCapabilities,
				CapsConfigured: CAP_NMEA_IN, // assume GPS by default (e.g. OGN Tracker)
			}
			if modelName == "Stratux_Serialout" {
				cfg.CapsConfigured = CAP_GDL90_OUT
			} else if modelName == "Stratux_Serialout_NMEA" {
				cfg.CapsConfigured = CAP_NMEA_OUT
			}
			return cfg
		}
	}
	return nil
}


// Checks if a detected udev device is an RTLSDR or UAT Radio.
// If it is, we check if the device was already configured previously and return that config if it was.
// Otherwise we return the devices default config based on Serial (auto detection, since Stratux SDRs are delivered with 
// serials like stx:1090:20)
func (manager* DeviceConfigManagerStruct) checkSdrDongle(dev *udev.Device) *SdrDeviceConfig  {
	props := dev.Properties()
	if props["DEVTYPE"] == "usb_device" && props["ID_VENDOR_ID"] == "0bda" && props["ID_MODEL_ID"] == "2838" {
		// RTLSDR device. Check if already configured
		serial := props["ID_SERIAL"]
		if cfg, ok := globalSettings.Devices.Sdrs[serial]; ok {
			return &cfg
		}
		// New device. Init with default config
		cfg := SdrDeviceConfig {
			Identifier : serial,
			HumanReadableName: strings.ReplaceAll(props["ID_SERIAL"], "_", " "),
			Serial: props["ID_SERIAL_SHORT"],
			Capabilities: CAP_1090ES | CAP_UAT | CAP_OGN,
			DevPath: props["DEVPATH"],
		}

		if rUAT.hasID(cfg.Serial) {
			cfg.CapsConfigured = CAP_UAT
		} else if rES.hasID(cfg.Serial) {
			cfg.CapsConfigured = CAP_1090ES
		} else if rOGN.hasID(cfg.Serial) {
			cfg.CapsConfigured = CAP_OGN
		}
		cfg.PPM = getPPM(cfg.Serial)
		return &cfg
	} else if props["SUBSYSTEM"] == "tty" && props["ID_VENDOR_ID"] == "0403" && props["ID_MODEL_ID"] == "7028" {
		// Stratux UAT Radio
		serial := props["ID_SERIAL"]
		if cfg, ok := globalSettings.Devices.Sdrs[serial]; ok {
			return &cfg
		}

		// New UAT Radio. Init with default config
		cfg := SdrDeviceConfig {
			Identifier: serial,
			HumanReadableName: strings.ReplaceAll(props["ID_SERIAL"], "_", " "),
			Serial: props["ID_SERIAL_SHORT"],
			Capabilities: CAP_UAT,
			DevPath: props["DEVPATH"],
			CapsConfigured: CAP_UAT,
		}
		return &cfg
	}
	return nil
}

func initDeviceManager() {
	DeviceConfigManager.listeners = make(map[DeviceEventListener]bool)
	DeviceConfigManager.Sdrs = make(map[string]SdrDevice)
	DeviceConfigManager.Serials = make(map[string]SerialDevice)
}

// Enumerates all devices with UDEV and then blocks while monitoring udev events
func detectDevices() {
	DeviceConfigManager.DetectConnectedDevices()
	DeviceConfigManager.MonitorDevices()
}