/*
	Copyright (c) 2015-2016 Christopher Young
	Distributable under the terms of The "BSD New" License
	that can be found in the LICENSE file, herein included
	as part of this header.

	sdr.go: SDR monitoring, SDR management, data input from UAT/1090ES channels.
*/

package main

import (
	"bufio"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/b3nn0/stratux/godump978"
	rtl "github.com/jpoirier/gortlsdr"
)

type SdrDevice interface {
	GetDeviceConfig() SdrDeviceConfig
	OnConfigUpdate(cfg SdrDeviceConfig)
	Shutdown()
}

// Device holds per dongle values and attributes
type SdrDeviceData struct {
	dev     *rtl.Context
	wg      *sync.WaitGroup
	closeCh chan int
	DeviceConfig SdrDeviceConfig
}


func (dev *SdrDeviceData) GetDeviceConfig() SdrDeviceConfig {
	return dev.DeviceConfig
}
func (dev *SdrDeviceData) OnConfigUpdate(cfg SdrDeviceConfig) {
	// For simplicity, we just shut the device down and re-detect it
	dev.Shutdown()
	sdrWatcher.onSdrAvailable(cfg)
}
func (dev *SdrDeviceData) Shutdown() {
	// Implemented by subtypes
}

// UAT is a 978 MHz device
type UAT struct {
	SdrDeviceData
} 

// ES is a 1090 MHz device
type ES struct {
	SdrDeviceData
}

// OGN is an 868 MHz device
type OGN struct {
	SdrDeviceData
}

type Dump1090TermMessage struct {
	Text   string
	Source string
}

func (e *ES) read() {
	defer e.wg.Done()
	log.Println("Entered ES read() ...")
	port := getFreeTcpPort()

	cmd := exec.Command("/usr/bin/dump1090", "--oversample", "--net-stratux-port", strconv.Itoa(int(port)),  "--net", "--device", e.DeviceConfig.Serial, "--ppm", string(e.DeviceConfig.PPM))
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	err := cmd.Start()
	if err != nil {
		log.Printf("Error executing /usr/bin/dump1090: %s\n", err)
		// don't return immediately, use the proper shutdown procedure
		for {
			select {
			case <-e.closeCh:
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	}

	log.Println("Executed /usr/bin/dump1090 successfully...")

	done := make(chan bool)

	go func() {
		for {
			select {
			case <-done:
				return
			case <-e.closeCh:
				log.Println("ES read(): shutdown msg received, calling cmd.Process.Kill() ...")
				err := cmd.Process.Kill()
				if err == nil {
					log.Println("kill successful...")
				}
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	}()

	stdoutBuf := make([]byte, 1024)
	stderrBuf := make([]byte, 1024)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				n, err := stdout.Read(stdoutBuf)
				if err == nil && n > 0 {
					m := Dump1090TermMessage{Text: string(stdoutBuf[:n]), Source: "stdout"}
					logDump1090TermMessage(m)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-done:
				return
			default:
				n, err := stderr.Read(stderrBuf)
				if err == nil && n > 0 {
					m := Dump1090TermMessage{Text: string(stderrBuf[:n]), Source: "stderr"}
					logDump1090TermMessage(m)
				}
			}
		}
	}()

	reader := NewDump1090Reader(port)
	go reader.run()

	cmd.Wait()
	reader.stop()

	// we get here if A) the dump1090 process died
	// on its own or B) cmd.Process.Kill() was called
	// from within the goroutine, either way close
	// the "done" channel, which ensures we don't leak
	// goroutines...
	close(done)
}

func (u *UAT) read() {
	defer u.wg.Done()
	log.Println("Entered UAT read() ...")
	var buffer = make([]uint8, rtl.DefaultBufLength)

	for {
		select {
		default:
			nRead, err := u.dev.ReadSync(buffer, rtl.DefaultBufLength)
			if err != nil {
				if globalSettings.DEBUG {
					log.Printf("\tReadSync Failed - error: %s\n", err)
				}
				break
			}

			if nRead > 0 {
				buf := buffer[:nRead]
				godump978.InChan <- buf
			}
		case <-u.closeCh:
			log.Println("UAT read(): shutdown msg received...")
			return
		}
	}
}

func (f *OGN) read() {
	defer f.wg.Done()
	log.Println("Entered OGN read() ...")
	port := getFreeTcpPort()

	// Check if http interface is already busy..
	httpPort := 8082
	if isPortListeningTcp(8082) {
		httpPort = 0
	}

	cmd := exec.Command("/usr/bin/ogn-rx-eu", "-s", f.DeviceConfig.Serial, "-p", strconv.Itoa(f.DeviceConfig.PPM), "-P",
		strconv.Itoa(int(port)), "-H", strconv.Itoa(httpPort), "-L/var/log/")

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	autoRestart := true // automatically restart crashing child process

	err := cmd.Start()
	if err != nil {
		log.Printf("OGN: Error executing ogn-rx-eu: %s\n", err)
		// don't return immediately, use the proper shutdown procedure
		autoRestart = false
		for {
			select {
			case <-f.closeCh:
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	}

	log.Println("OGN: Executed ogn-rx-eu successfully...")

	done := make(chan bool)

	go func() {
		for {
			select {
			case <-done:
				return
			case <-f.closeCh:
				log.Println("OGN read(): shutdown msg received, calling cmd.Process.Kill() ...")
				autoRestart = false
				err := cmd.Process.Kill()
				if err == nil {
					log.Println("kill successful...")
				}
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	}()

	go func() {
		reader := bufio.NewReader(stdout)
		for {
			select {
			case <-done:
				return
			default:
				line, err := reader.ReadString('\n')
				line = strings.TrimSpace(line)
				if err == nil  && len(line) > 0 /* && globalSettings.DEBUG */ {
					log.Println("OGN: ogn-rx-eu stdout: ", line)
				}
			}
		}
	}()

	go func() {
		reader := bufio.NewReader(stderr)
		for {
			select {
			case <-done:
				return
			default:
				line, err := reader.ReadString('\n')
				if err == nil {
					log.Println("OGN: ogn-rx-eu stderr: ", strings.TrimSpace(line))
				}
			}
		}
	}()

	ognReader := NewOgnReader(port)
	go ognReader.run()

	cmd.Wait()

	log.Println("OGN: ogn-rx-eu terminated...")
	ognReader.stop()

	// we get here if A) the ogn-rx-eu process died
	// on its own or B) cmd.Process.Kill() was called
	// from within the goroutine, either way close
	// the "done" channel, which ensures we don't leak
	// goroutines...
	close(done)

	if autoRestart {
		time.Sleep(5 * time.Second)
		log.Println("OGN: restarting crashed ogn-rx-eu")
		f.wg.Add(1)
		go f.read()
	}
}

// 978 UAT configuration settings
const (
	TunerGain    = 480
	SampleRate   = 2083334
	NewRTLFreq   = 28800000
	NewTunerFreq = 28800000
	CenterFreq   = 978000000
	Bandwidth    = 1000000
)

func (u *UAT) sdrConfig() (err error) {
	devId, err := rtl.GetIndexBySerial(u.DeviceConfig.Serial)
	if err != nil {
		log.Printf("UAT GetIndexBySerial failed...")
		return
	}
	log.Printf("===== UAT Device Name  : %s =====\n", rtl.GetDeviceName(devId))
	log.Printf("===== UAT Device Serial: %s=====\n", u.DeviceConfig.Identifier)

	if u.dev, err = rtl.Open(devId); err != nil {
		log.Printf("\tUAT Open Failed...\n")
		return
	}
	log.Printf("\tGetTunerType: %s\n", u.dev.GetTunerType())

	//---------- Set Tuner Gain ----------
	err = u.dev.SetTunerGainMode(true)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetTunerGainMode Failed - error: %s\n", err)
		return
	}
	log.Printf("\tSetTunerGainMode Successful\n")

	err = u.dev.SetTunerGain(TunerGain)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetTunerGain Failed - error: %s\n", err)
		return
	}
	log.Printf("\tSetTunerGain Successful\n")

	tgain := u.dev.GetTunerGain()
	log.Printf("\tGetTunerGain: %d\n", tgain)

	//---------- Get/Set Sample Rate ----------
	err = u.dev.SetSampleRate(SampleRate)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetSampleRate Failed - error: %s\n", err)
		return
	}
	log.Printf("\tSetSampleRate - rate: %d\n", SampleRate)

	log.Printf("\tGetSampleRate: %d\n", u.dev.GetSampleRate())

	//---------- Get/Set Xtal Freq ----------
	rtlFreq, tunerFreq, err := u.dev.GetXtalFreq()
	if err != nil {
		u.dev.Close()
		log.Printf("\tGetXtalFreq Failed - error: %s\n", err)
		return
	}
	log.Printf("\tGetXtalFreq - Rtl: %d, Tuner: %d\n", rtlFreq, tunerFreq)

	err = u.dev.SetXtalFreq(NewRTLFreq, NewTunerFreq)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetXtalFreq Failed - error: %s\n", err)
		return
	}
	log.Printf("\tSetXtalFreq - Center freq: %d, Tuner freq: %d\n",
		NewRTLFreq, NewTunerFreq)

	//---------- Get/Set Center Freq ----------
	err = u.dev.SetCenterFreq(CenterFreq)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetCenterFreq 978MHz Failed, error: %s\n", err)
		return
	}
	log.Printf("\tSetCenterFreq 978MHz Successful\n")

	log.Printf("\tGetCenterFreq: %d\n", u.dev.GetCenterFreq())

	//---------- Set Bandwidth ----------
	log.Printf("\tSetting Bandwidth: %d\n", Bandwidth)
	if err = u.dev.SetTunerBw(Bandwidth); err != nil {
		u.dev.Close()
		log.Printf("\tSetTunerBw %d Failed, error: %s\n", Bandwidth, err)
		return
	}
	log.Printf("\tSetTunerBw %d Successful\n", Bandwidth)

	if err = u.dev.ResetBuffer(); err != nil {
		u.dev.Close()
		log.Printf("\tResetBuffer Failed - error: %s\n", err)
		return
	}
	log.Printf("\tResetBuffer Successful\n")

	//---------- Get/Set Freq Correction ----------
	freqCorr := u.dev.GetFreqCorrection()
	log.Printf("\tGetFreqCorrection: %d\n", freqCorr)

	err = u.dev.SetFreqCorrection(u.DeviceConfig.PPM)
	if err != nil {
		u.dev.Close()
		log.Printf("\tSetFreqCorrection %d Failed, error: %s\n", u.DeviceConfig.PPM, err)
		return
	}
	log.Printf("\tSetFreqCorrection %d Successful\n", u.DeviceConfig.PPM)

	return
}

// Read from the godump978 channel - on or off.
func uatReader() {
	log.Println("Entered uatReader() ...")
	for {
		uat := <-godump978.OutChan
		o, msgtype := parseInput(uat)
		if o != nil && msgtype != 0 {
			relayMessage(msgtype, o)
		}
	}
}

func (u *UAT) writeID() error {
	info, err := u.dev.GetHwInfo()
	if err != nil {
		return err
	}
	info.Serial = "stratux:978"
	return u.dev.SetHwInfo(info)
}

func (e *ES) writeID() error {
	info, err := e.dev.GetHwInfo()
	if err != nil {
		return err
	}
	info.Serial = "stratux:1090"
	return e.dev.SetHwInfo(info)
}

func (f *OGN) writeID() error {
	info, err := f.dev.GetHwInfo()
	if err != nil {
		return err
	}
	info.Serial = "stratux:868"
	return f.dev.SetHwInfo(info)
}

func (u *UAT) Shutdown() {
	log.Println("Entered UAT shutdown() ...")
	close(u.closeCh) // signal to shutdown
	log.Println("UAT shutdown(): calling u.wg.Wait() ...")
	u.wg.Wait() // Wait for the goroutine to shutdown
	log.Println("UAT shutdown(): u.wg.Wait() returned...")
	log.Println("UAT shutdown(): closing device ...")
	u.dev.Close() // preempt the blocking ReadSync call
	log.Println("UAT shutdown() complete ...")
	DeviceConfigManager.onSdrDeviceConnected(u)
}

func (e *ES) Shutdown() {
	log.Println("Entered ES shutdown() ...")
	close(e.closeCh) // signal to shutdown
	log.Println("ES shutdown(): calling e.wg.Wait() ...")
	e.wg.Wait() // Wait for the goroutine to shutdown
	log.Println("ES shutdown() complete ...")
	DeviceConfigManager.onSdrDeviceConnected(e)
}

func (f *OGN) Shutdown() {
	log.Println("Entered OGN shutdown() ...")
	close(f.closeCh) // signal to shutdown
	log.Println("signal shutdown(): calling f.wg.Wait() ...")
	f.wg.Wait() // Wait for the goroutine to shutdown
	log.Println("signal shutdown() complete ...")
	DeviceConfigManager.onSdrDeviceConnected(f)
}


func createUATDev(cfg SdrDeviceConfig) (*UAT, error) {
	UATDev := &UAT{SdrDeviceData {DeviceConfig: cfg}}
	if err := UATDev.sdrConfig(); err != nil {
		log.Printf("UATDev.sdrConfig() failed: %s\n", err)
		UATDev = nil
		return nil, err
	}
	UATDev.wg = &sync.WaitGroup{}
	UATDev.closeCh = make(chan int)
	UATDev.wg.Add(1)
	go UATDev.read()
	DeviceConfigManager.onSdrDeviceConnected(UATDev)
	return UATDev, nil
}

func createESDev(cfg SdrDeviceConfig) (*ES, error) {
	ESDev := &ES{SdrDeviceData {DeviceConfig: cfg}}
	ESDev.wg = &sync.WaitGroup{}
	ESDev.closeCh = make(chan int)
	ESDev.wg.Add(1)
	go ESDev.read()
	DeviceConfigManager.onSdrDeviceConnected(ESDev)
	return ESDev, nil
}

func createOGNDev(cfg SdrDeviceConfig) (*OGN, error) {
	OGNDev := &OGN{SdrDeviceData {DeviceConfig: cfg}}
	OGNDev.wg = &sync.WaitGroup{}
	OGNDev.closeCh = make(chan int)
	OGNDev.wg.Add(1)
	go OGNDev.read()
	DeviceConfigManager.onSdrDeviceConnected(OGNDev)
	return OGNDev, nil
}

type SdrWatcher struct {
}
var sdrWatcher SdrWatcher

func (w *SdrWatcher) onSdrAvailable(cfg SdrDeviceConfig) {
	if cfg.CapsConfigured & CAP_1090ES > 0 {
		createESDev(cfg)
	} else if cfg.CapsConfigured & CAP_UAT > 0 {
		createUATDev(cfg)
	} else if cfg.CapsConfigured & CAP_OGN > 0 {
		createOGNDev(cfg)
	}
}

// Not needed, interface function
func (w *SdrWatcher) onSerialAvailable(cfg SerialDeviceConfig, props map[string]string) {}


func sdrInit() {
	DeviceConfigManager.AddDeviceEventListener(&sdrWatcher)
	go uatReader()
	go godump978.ProcessDataFromChannel()
}
