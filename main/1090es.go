package main

import (
	"encoding/json"
	"log"
	"math"
	"strings"

	"github.com/b3nn0/stratux/common"
)


type Dump1090Reader struct {
	tcpReader *TcpLineReader
	rxChan chan string
	txChan chan string
}

func NewDump1090Reader(port uint16) *Dump1090Reader {
	return &Dump1090Reader{
		NewTcpLineReader(port),
		make(chan string, 100),
		make(chan string),
	}
}

func (reader *Dump1090Reader) run() {
	go reader.tcpReader.run(reader.rxChan, reader.txChan)

	loop: for {
		select {
		case data, ok := <- reader.rxChan:
			if !ok {
				break loop
			}
			importDump1090Message(data)
		}
	}
}

func (reader *Dump1090Reader) stop() {
	reader.tcpReader.close()
}


func importDump1090Message(buf string) {
	buf = strings.Trim(buf, "\r\n")

	// Log the message to the message counter in any case.
	var thisMsg msg
	thisMsg.MessageClass = MSGCLASS_ES
	thisMsg.TimeReceived = stratuxClock.Time
	thisMsg.Data = buf
	msgLogAppend(thisMsg)

	var eslog esmsg
	eslog.TimeReceived = stratuxClock.Time
	eslog.Data = buf
	logESMsg(eslog) // log raw dump1090:30006 output to SQLite log

	var newTi *dump1090Data
	err := json.Unmarshal([]byte(buf), &newTi)
	if err != nil {
		log.Printf("can't read ES traffic information from %s: %s\n", buf, err.Error())
		return
	}

	if newTi.Icao_addr == 0x07FFFFFF { // used to signal heartbeat
		if globalSettings.DEBUG {
			log.Printf("No traffic last 60 seconds. Heartbeat message from dump1090: %s\n", buf)
		}
		return // don't process heartbeat messages
	}

	if (newTi.Icao_addr & 0x01000000) != 0 { // bit 25 used by dump1090 to signal non-ICAO address
		newTi.Icao_addr = newTi.Icao_addr & 0x00FFFFFF
		if globalSettings.DEBUG {
			log.Printf("Non-ICAO address %X sent by dump1090. This is typical for TIS-B.\n", newTi.Icao_addr)
		}
	}
	icao := uint32(newTi.Icao_addr)
	var ti TrafficInfo

	trafficMutex.Lock()

	// Retrieve previous information on this ICAO code.
	if val, ok := traffic[icao]; ok { // if we've already seen it, copy it in to do updates
		ti = val
		//log.Printf("Existing target %X imported for ES update\n", icao)
	} else {
		//log.Printf("New target %X created for ES update\n",newTi.Icao_addr)
		ti.Last_seen = stratuxClock.Time // need to initialize to current stratuxClock so it doesn't get cut before we have a chance to populate a position message
		ti.Last_alt = stratuxClock.Time  // ditto.
		ti.Icao_addr = icao
		ti.ExtrapolatedPosition = false
		ti.Last_source = TRAFFIC_SOURCE_1090ES

		thisReg, validReg := icao2reg(icao)
		if validReg {
			ti.Reg = thisReg
			ti.Tail = thisReg
		}
	}

	if newTi.SignalLevel > 0 {
		ti.SignalLevel = 10 * math.Log10(newTi.SignalLevel)
	} else {
		ti.SignalLevel = -999
	}

	// generate human readable summary of message types for debug
	//TODO: Use for ES message statistics?
	/*
		var s1 string
		if newTi.DF == 17 {
			s1 = "ADS-B"
		}
		if newTi.DF == 18 {
			s1 = "ADS-R / TIS-B"
		}

		if newTi.DF == 4 || newTi.DF == 20 {
			s1 = "Surveillance, Alt. Reply"
		}

		if newTi.DF == 5 || newTi.DF == 21 {
			s1 = "Surveillance, Ident. Reply"
		}

		if newTi.DF == 11 {
			s1 = "All-call Reply"
		}

		if newTi.DF == 0 {
			s1 = "Short Air-Air Surv."
		}

		if newTi.DF == 16 {
			s1 = "Long Air-Air Surv."
		}
	*/
	//log.Printf("Mode S message from icao=%X, DF=%02d, CA=%02d, TC=%02d (%s)\n", ti.Icao_addr, newTi.DF, newTi.CA, newTi.TypeCode, s1)

	// Altitude will be sent by dump1090 for ES ADS-B/TIS-B (DF=17 and DF=18)
	// and Mode S messages (DF=0, DF = 4, and DF = 20).

	ti.AltIsGNSS = newTi.AltIsGNSS

	if newTi.Alt != nil {
		ti.Alt = int32(*newTi.Alt)
		ti.Last_alt = stratuxClock.Time
	}

	if newTi.GnssDiffFromBaroAlt != nil {
		ti.GnssDiffFromBaroAlt = int32(*newTi.GnssDiffFromBaroAlt) // we can estimate pressure altitude from GNSS height with this parameter!
		ti.Last_GnssDiff = stratuxClock.Time
		ti.Last_GnssDiffAlt = ti.Alt
	}

	// Position updates are provided only by ES messages (DF=17 and DF=18; multiple TCs)
	if newTi.Position_valid { // i.e. DF17 or DF18 message decoded successfully by dump1090
		valid_position := true
		var lat, lng float32

		if newTi.Lat != nil {
			lat = float32(*newTi.Lat)
		} else { // dump1090 send a valid message, but Stratux couldn't figure it out for some reason.
			valid_position = false
			//log.Printf("Missing latitude in DF=17/18 airborne position message\n")
		}

		if newTi.Lng != nil {
			lng = float32(*newTi.Lng)
		} else { //
			valid_position = false
			//log.Printf("Missing longitude in DF=17 airborne position message\n")
		}

		if valid_position {
			ti.Lat = lat
			ti.Lng = lng
			if isGPSValid() {
				ti.Distance, ti.Bearing = common.Distance(float64(mySituation.GPSLatitude), float64(mySituation.GPSLongitude), float64(ti.Lat), float64(ti.Lng))
				ti.BearingDist_valid = true
			}
			ti.Position_valid = true
			ti.ExtrapolatedPosition = false
			ti.Last_seen = stratuxClock.Time // only update "last seen" data on position updates
		}
	} else {
		// Old traffic had no position and update doesn't have a position either -> assume Mode-S only
		if !ti.Position_valid {
			ti.Last_seen = ti.Last_alt
		}
	}

	if newTi.Speed_valid { // i.e. DF17 or DF18, TC 19 message decoded successfully by dump1090
		valid_speed := true
		var speed uint16
		var track float32

		if newTi.Track != nil {
			track = float32(*newTi.Track)
		} else { // dump1090 send a valid message, but Stratux couldn't figure it out for some reason.
			valid_speed = false
			//log.Printf("Missing track in DF=17/18 TC19 airborne velocity message\n")
		}

		if newTi.Speed != nil {
			speed = uint16(*newTi.Speed)
		} else { //
			valid_speed = false
			//log.Printf("Missing speed in DF=17/18 TC19 airborne velocity message\n")
		}

		if newTi.Vvel != nil {
			ti.Vvel = int16(*newTi.Vvel)
		} else { // we'll still make the message without a valid vertical speed.
			//log.Printf("Missing vertical speed in DF=17/18 TC19 airborne velocity message\n")
		}

		if valid_speed {
			ti.Track = track
			ti.Speed = speed
			ti.Speed_valid = true
			ti.Last_speed = stratuxClock.Time // only update "last seen" data on position updates
		}
	} else if ((newTi.DF == 17) || (newTi.DF == 18)) && (newTi.TypeCode == 19) { // invalid speed on velocity message only
		ti.Speed_valid = false
	}

	// Determine NIC (navigation integrity category) from type code and subtype code
	if ((newTi.DF == 17) || (newTi.DF == 18)) && (newTi.TypeCode >= 5 && newTi.TypeCode <= 22) && (newTi.TypeCode != 19) {
		nic := 0 // default for unknown or missing NIC
		switch newTi.TypeCode {
		case 0, 8, 18, 22:
			nic = 0
		case 17:
			nic = 1
		case 16:
			if newTi.SubtypeCode == 1 {
				nic = 3
			} else {
				nic = 2
			}
		case 15:
			nic = 4
		case 14:
			nic = 5
		case 13:
			nic = 6
		case 12:
			nic = 7
		case 11:
			if newTi.SubtypeCode == 1 {
				nic = 9
			} else {
				nic = 8
			}
		case 10, 21:
			nic = 10
		case 9, 20:
			nic = 11
		}
		ti.NIC = nic

		if (ti.NACp < 7) && (ti.NACp < ti.NIC) {
			ti.NACp = ti.NIC // initialize to NIC, since NIC is sent with every position report, and not all emitters report NACp.
		}
	}

	if newTi.NACp != nil {
		ti.NACp = *newTi.NACp
	}

	if newTi.Emitter_category != nil {
		ti.Emitter_category = uint8(*newTi.Emitter_category) // validate dump1090 on live traffic
	}

	if newTi.Squawk != nil {
		ti.Squawk = int(*newTi.Squawk) // only provided by Mode S messages, so we don't do this in parseUAT.
	}
	// Set the target type. DF=18 messages are sent by ground station, so we look at CA
	// (repurposed to Control Field in DF18) to determine if it's ADS-R or TIS-B.
	if newTi.DF == 17 {
		ti.TargetType = TARGET_TYPE_ADSB
		ti.Addr_type = 0
	} else if newTi.DF == 18 {
		if newTi.CA == 6 {
			ti.TargetType = TARGET_TYPE_ADSR
			ti.Addr_type = 2
		} else if newTi.CA == 2 { // 2 = TIS-B with ICAO address, 5 = TIS-B without ICAO address
			ti.TargetType = TARGET_TYPE_TISB
			ti.Addr_type = 2
		} else if newTi.CA == 5 {
			ti.TargetType = TARGET_TYPE_TISB
			ti.Addr_type = 3
		}
	}

	if newTi.OnGround != nil { // DF=11 messages don't report "on ground" status so we need to check for valid values.
		ti.OnGround = bool(*newTi.OnGround)
	}

	if (newTi.Tail != nil) && ((newTi.DF == 17) || (newTi.DF == 18) || (newTi.DF == 20) || (newTi.DF == 21)) { // DF=17 or DF=18, Type Code 1-4 , DF=20 Altitude Reply (often with Ident in Comm-B) DF=21 Identity Reply
		ti.Tail = *newTi.Tail
		ti.Tail = strings.Trim(ti.Tail, " ") // remove extraneous spaces
	}

	// This is a hack to show the source of the traffic on moving maps.

	if globalSettings.DisplayTrafficSource {
		type_code := " "
		switch ti.TargetType {
		case TARGET_TYPE_ADSB:
			type_code = "a"
		case TARGET_TYPE_ADSR:
			type_code = "r"
		case TARGET_TYPE_TISB:
			type_code = "t"
		}

		if len(ti.Tail) == 0 {
			ti.Tail = "e" + type_code
		} else if len(ti.Tail) < 7 && ti.Tail[0] != 'e' && ti.Tail[0] != 'u' {
			ti.Tail = "e" + type_code + ti.Tail
		} else if len(ti.Tail) == 7 && ti.Tail[0] != 'e' && ti.Tail[0] != 'u' {
			ti.Tail = "e" + type_code + ti.Tail[1:]
		} else if len(ti.Tail) > 1 { // bounds checking
			ti.Tail = "e" + type_code + ti.Tail[2:]

		}
	}

	if newTi.DF == 17 || newTi.DF == 18 {
		ti.Last_source = TRAFFIC_SOURCE_1090ES // only update traffic source on ADS-B messages. Prevents source on UAT ADS-B targets with Mode S transponders from "flickering" every time we get an altitude or DF11 update.
	}
	ti.Timestamp = newTi.Timestamp // only update "last seen" data on position updates

	/*
		s_out, err := json.Marshal(ti)
		if err != nil {
			log.Printf("Error generating output: %s\n", err.Error())
		} else {
			log.Printf("%X (DF%d) => %s\n", ti.Icao_addr, newTi.DF, string(s_out))
		}
	*/
	postProcessTraffic(&ti)
	traffic[ti.Icao_addr] = ti // Update information on this ICAO code.
	registerTrafficUpdate(ti)
	seenTraffic[ti.Icao_addr] = true // Mark as seen.
	//log.Printf("%v\n",traffic)
	trafficMutex.Unlock()
}
