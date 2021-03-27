package main

import (
	"bufio"
	"log"
	"net"
	"strconv"
	"time"
)

// Helper to read line-based data from a TCP socket. Used to read from
// dump1090 or ogn-rx-eu.
// Additionally also allows writing to the socket (e.g. to provide NMEA to ogn-rx-eu)
type TcpLineReader struct {
	Port uint16
	IsConnected bool
	stopChan chan bool
}

func NewTcpLineReader(port uint16) *TcpLineReader {
	return &TcpLineReader{port, false, make(chan bool, 100)}
}

// go routine to start work
func (reader *TcpLineReader) run(rxChan chan string, txChan chan string) {
	log.Printf("Connecting TCP port %d", reader.Port)

	addr := "127.0.0.1:" + strconv.Itoa(int(reader.Port))
	conn, err := net.Dial("tcp", addr)

	connectLoop: for err != nil { // Connection failed?
		conn, err = net.Dial("tcp", addr)

		select {
		case <- time.After(3 * time.Second):
			continue connectLoop // try to connect again
		case <- reader.stopChan: // stop connect loop if channel closed
			return
		}
	}

	log.Printf("Port %d connected", reader.Port)
	sockReader := bufio.NewReader(conn)
	sockWriter := bufio.NewWriter(conn)
	reader.IsConnected = true

	// Socket reader
	go func() {
		for {
			line, err := sockReader.ReadString('\n')
			if err != nil {
				log.Printf("Socket connection lost: " + err.Error())
				break;
			}
			rxChan <- line
		}
	}()

	// Write and handle exit case
	loop: for {
		select {
		case data := <- txChan:
			sockWriter.Write([]byte(data))
			sockWriter.Flush()
		case <- reader.stopChan:
			break loop
		}
	}
	reader.IsConnected = false
	close(rxChan)
	conn.Close() // will also close the reader goroutine
}

func (reader *TcpLineReader) close() {
	reader.stopChan <- true
}