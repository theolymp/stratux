/*
	Copyright (c) 2015-2016 Christopher Young
	Distributable under the terms of The "BSD New" License
	that can be found in the LICENSE file, herein included
	as part of this header.

	datalog.go: Log stratux data as it is received. Bucket data into timestamp time slots.

*/

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type StratuxTimestamp struct {
	id                   int64
	StratuxClock_value   int64
	SystemClock_value    int64 // Only if we consider it valid (i.e. had GPS Signal since startup)
	GPSClock_value       int64 // The value of this is either from the GPS or extrapolated from the GPS via stratuxClock if pref is 1 or 2. It is time.Time{} if 0.
	StartupID            int64
}

// 'startup' table creates a new entry each time the daemon is started. This keeps track of sequential starts, even if the
//  timestamp is ambiguous (units with no GPS). This struct is just a placeholder for an empty table (other than primary key).
type StratuxStartup struct {
	id   int64
	Fill string
}

var dataLogStarted bool
var dataLogReadyToWrite bool

var stratuxStartupID int64

type SQLiteMarshal struct {
	FieldType string
	Marshal   func(v reflect.Value) string
}

func boolMarshal(v reflect.Value) string {
	b := v.Bool()
	if b {
		return "1"
	}
	return "0"
}

func intMarshal(v reflect.Value) string {
	return strconv.FormatInt(v.Int(), 10)
}

func uintMarshal(v reflect.Value) string {
	return strconv.FormatUint(v.Uint(), 10)
}

func floatMarshal(v reflect.Value) string {
	return strconv.FormatFloat(v.Float(), 'f', 10, 64)
}

func stringMarshal(v reflect.Value) string {
	return v.String()
}

func notsupportedMarshal(v reflect.Value) string {
	return ""
}

func structCanBeMarshalled(v reflect.Value) bool {
	m := v.MethodByName("String")
	if m.IsValid() && !m.IsNil() {
		return true
	}
	return false
}

func structMarshal(v reflect.Value) string {
	if structCanBeMarshalled(v) {
		m := v.MethodByName("String")
		in := make([]reflect.Value, 0)
		ret := m.Call(in)
		// hack: time value that's uninitialized -- we want that as null
		if len(ret) > 0 {
			s := ret[0].String()
			if s != "0001-01-01 00:00:00 +0000 UTC" {
				return s
			}
		}
	}
	return ""
}

var sqliteMarshalFunctions = map[string]SQLiteMarshal{
	"bool":         {FieldType: "INTEGER", Marshal: boolMarshal},
	"int":          {FieldType: "INTEGER", Marshal: intMarshal},
	"uint":         {FieldType: "INTEGER", Marshal: uintMarshal},
	"float":        {FieldType: "REAL", Marshal: floatMarshal},
	"string":       {FieldType: "TEXT", Marshal: stringMarshal},
	"struct":       {FieldType: "STRING", Marshal: structMarshal},
	"notsupported": {FieldType: "notsupported", Marshal: notsupportedMarshal},
}

var sqlTypeMap = map[reflect.Kind]string{
	reflect.Bool:          "bool",
	reflect.Int:           "int",
	reflect.Int8:          "int",
	reflect.Int16:         "int",
	reflect.Int32:         "int",
	reflect.Int64:         "int",
	reflect.Uint:          "uint",
	reflect.Uint8:         "uint",
	reflect.Uint16:        "uint",
	reflect.Uint32:        "uint",
	reflect.Uint64:        "uint",
	reflect.Uintptr:       "notsupported",
	reflect.Float32:       "float",
	reflect.Float64:       "float",
	reflect.Complex64:     "notsupported",
	reflect.Complex128:    "notsupported",
	reflect.Array:         "notsupported",
	reflect.Chan:          "notsupported",
	reflect.Func:          "notsupported",
	reflect.Interface:     "notsupported",
	reflect.Map:           "notsupported",
	reflect.Ptr:           "notsupported",
	reflect.Slice:         "notsupported",
	reflect.String:        "string",
	reflect.Struct:        "struct",
	reflect.UnsafePointer: "notsupported",
}

func makeTable(i interface{}, tbl string, db *sql.DB) {
	val := reflect.ValueOf(i)

	fields := make([]string, 0)
	for i := 0; i < val.NumField(); i++ {
		kind := val.Field(i).Kind()
		fieldName := val.Type().Field(i).Name
		sqlTypeAlias := sqlTypeMap[kind]

		// Check that if the field is a struct that it can be marshalled.
		if sqlTypeAlias == "struct" && !structCanBeMarshalled(val.Field(i)) {
			continue
		}
		if sqlTypeAlias == "notsupported" || fieldName == "id" {
			continue
		}
		sqlType := sqliteMarshalFunctions[sqlTypeAlias].FieldType
		s := fieldName + " " + sqlType
		fields = append(fields, s)
	}

	// Add the timestamp_id field to link up with the timestamp table.
	if tbl != "timestamp" && tbl != "startup" {
		fields = append(fields, "timestamp_id INTEGER")
	}

	tblCreate := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, %s)", tbl, strings.Join(fields, ", "))
	_, err := db.Exec(tblCreate)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
	}

	// Now additionally, try to ALTER TABLE ADD COLUMN all the fields, ignoring errors. This is so we can
	// add new fields to existing datalog tables
	for _, field := range fields {
		tblAlter := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", tbl, field)
		db.Exec(tblAlter)
	}
}


/*
	bulkInsert().
		Reads insertBatch and insertBatchIfs. This is called after a group of insertData() calls.
*/

func bulkInsert(tbl string, db *sql.DB) (res sql.Result, err error) {
	if _, ok := insertString[tbl]; !ok {
		return nil, errors.New("no insert statement")
	}

	batchVals := insertBatchIfs[tbl]
	numColsPerRow := len(batchVals[0])
	maxRowBatch := int(999 / numColsPerRow) // SQLITE_MAX_VARIABLE_NUMBER = 999.
	//	log.Printf("table %s. %d cols per row. max batch %d\n", tbl, numColsPerRow, maxRowBatch)
	for len(batchVals) > 0 {
		//     timeInit := time.Now()
		i := int(0) // Variable number of rows per INSERT statement.

		stmt := ""
		vals := make([]interface{}, 0)
		querySize := uint64(0)                                            // Size of the query in bytes.
		for len(batchVals) > 0 && i < maxRowBatch && querySize < 750000 { // Maximum of 1,000,000 bytes per query.
			if len(stmt) == 0 { // The first set will be covered by insertString.
				stmt = insertString[tbl]
				querySize += uint64(len(insertString[tbl]))
			} else {
				addStr := ", (" + strings.Join(strings.Split(strings.Repeat("?", len(batchVals[0])), ""), ",") + ")"
				stmt += addStr
				querySize += uint64(len(addStr))
			}
			for _, val := range batchVals[0] {
				querySize += uint64(len(val.(string)))
			}
			vals = append(vals, batchVals[0]...)
			batchVals = batchVals[1:]
			i++
		}
		//		log.Printf("inserting %d rows to %s. querySize=%d\n", i, tbl, querySize)
		res, err = db.Exec(stmt, vals...)
		//      timeBatch := time.Since(timeInit)                                                                                                                     // debug
		//      log.Printf("SQLite: bulkInserted %d rows to %s. Took %f msec to build and insert query. querySize=%d\n", i, tbl, 1000*timeBatch.Seconds(), querySize) // debug
		if err != nil {
			log.Printf("sqlite INSERT error: '%s'\n", err.Error())
			return
		}
	}

	// Clear the buffers.
	delete(insertString, tbl)
	delete(insertBatchIfs, tbl)

	return
}

/*
	insertData().
		Inserts an arbitrary struct into an SQLite table.
		 Inserts the timestamp first, if its 'id' is 0.

*/

// Cached 'VALUES' statements. Indexed by table name.
var insertString map[string]string // INSERT INTO tbl (col1, col2, ...) VALUES(?, ?, ...). Only for one value.
var insertBatchIfs map[string][][]interface{}

func insertData(i interface{}, tbl string, db *sql.DB, ts_num int64) int64 {
	val := reflect.ValueOf(i)

	keys := make([]string, 0)
	values := make([]string, 0)
	for i := 0; i < val.NumField(); i++ {
		kind := val.Field(i).Kind()
		fieldName := val.Type().Field(i).Name
		sqlTypeAlias := sqlTypeMap[kind]

		if sqlTypeAlias == "notsupported" || fieldName == "id" {
			continue
		}

		v := sqliteMarshalFunctions[sqlTypeAlias].Marshal(val.Field(i))

		keys = append(keys, fieldName)
		values = append(values, v)
	}
	if tbl != "startup" && tbl != "timestamp" {
		keys = append(keys, "timestamp_id")
		values = append(values, fmt.Sprint(ts_num))
	}

	if _, ok := insertString[tbl]; !ok {
		// Prepare the statement.
		tblInsert := fmt.Sprintf("INSERT INTO %s (%s) VALUES(%s)", tbl, strings.Join(keys, ","),
			strings.Join(strings.Split(strings.Repeat("?", len(keys)), ""), ","))
		insertString[tbl] = tblInsert
	}

	// Make the values slice into a slice of interface{}.
	ifs := make([]interface{}, len(values))
	for i := 0; i < len(values); i++ {
		ifs[i] = values[i]
	}

	insertBatchIfs[tbl] = append(insertBatchIfs[tbl], ifs)

	if tbl == "timestamp" || tbl == "startup" { // Immediate insert always for "timestamp" and "startup" table.
		res, err := bulkInsert(tbl, db) // Bulk insert of 1, always.
		if err == nil {
			id, _ := res.LastInsertId()
			return id
		}
	}

	return 0
}

type DataLogRow struct {
	key    string // we only log rows once each configured interval if they have the same uniqueKey (e.g. "situation" or a traffic target's hex ID)
	tbl    string
	data   interface{}
}

var shutdownDataLog chan bool
var shutdownDataLogWriter chan bool

var dataLogWriteChan chan DataLogRow
var dataLogWriteBatchChan chan bool

var lastWriteTs StratuxTimestamp

func dataLogWriter(db *sql.DB) {
	dataLogWriteChan = make(chan DataLogRow, 10240)
	dataLogWriteBatchChan = make(chan bool, 1)
	shutdownDataLogWriter = make(chan bool)

	// Unkeyed rows - always write em all
	rowsQueuedForWrite := make([]DataLogRow, 0)
	// keyed rows - only write them once per time unit
	keyedRowsQueuedForWrite := make(map[string]DataLogRow)
	loop: for {
		select {
		case r := <-dataLogWriteChan:
			// Accept timestamped row.
			if r.key == "" {
				rowsQueuedForWrite = append(rowsQueuedForWrite, r)
			} else {
				keyedRowsQueuedForWrite[r.key] = r
			}
			
			currentMs := int64(stratuxClock.Milliseconds)
			if currentMs - lastWriteTs.StratuxClock_value < int64(globalSettings.ReplayLogResolutionMs) {
				// don't write yet
				continue
			}

			// perform write
			timeStart := stratuxClock.Time

			var ts StratuxTimestamp
			ts.id = 0
			ts.StratuxClock_value = currentMs
			if isGPSClockValid() {
				ts.GPSClock_value = mySituation.GPSTime.UnixNano() / int64(time.Millisecond)
			}
			if globalStatus.SystemClockValid {
				ts.SystemClock_value = time.Now().UTC().UnixNano() / int64(time.Millisecond)
			}
			ts.StartupID = stratuxStartupID
			ts.id = insertData(ts, "timestamp", db, 0)
			lastWriteTs = ts
			
			// Append the remaining keyed values
			for _, val := range keyedRowsQueuedForWrite {
				rowsQueuedForWrite = append(rowsQueuedForWrite, val)
			}

			nRows := len(rowsQueuedForWrite)
			if globalSettings.DEBUG {
				log.Printf("Writing %d rows\n", nRows)
			}
			// Write the buffered rows. This will block while it is writing.
			// Save the names of the tables affected so that we can run bulkInsert() on after the insertData() calls.
			tblsAffected := make(map[string]bool)
			// Start transaction.
			tx, err := db.Begin()
			if err != nil {
				log.Printf("db.Begin() error: %s\n", err.Error())
				break // from select {}
			}
			for _, r := range rowsQueuedForWrite {
				tblsAffected[r.tbl] = true
				insertData(r.data, r.tbl, db, ts.id)
			}
			// Do the bulk inserts.
			for tbl, _ := range tblsAffected {
				bulkInsert(tbl, db)
			}
			// Close the transaction.
			tx.Commit()
			// Zero the queues.
			keyedRowsQueuedForWrite = make(map[string]DataLogRow)
			rowsQueuedForWrite = make([]DataLogRow, 0) 
			timeElapsed := stratuxClock.Since(timeStart)
			if globalSettings.DEBUG {
				rowsPerSecond := float64(nRows) / float64(timeElapsed.Seconds())
				log.Printf("Writing finished. %d rows in %.2f seconds (%.1f rows per second).\n", nRows, float64(timeElapsed.Seconds()), rowsPerSecond)
			}
			if uint32(timeElapsed.Milliseconds()) > globalSettings.ReplayLogResolutionMs {
				log.Printf("WARNING! SQLite logging is behind. Last write took %.1f seconds.\n", float64(timeElapsed.Seconds()))
				dataLogCriticalErr := fmt.Errorf("WARNING! SQLite logging is behind. Last write took %.1f seconds.\n", float64(timeElapsed.Seconds()))
				addSystemError(dataLogCriticalErr)
			}
		case <-shutdownDataLogWriter: // Received a message on the channel to initiate a graceful shutdown, and to command dataLog() to shut down
			log.Printf("datalog.go: dataLogWriter() received shutdown message with rowsQueuedForWrite = %d\n", len(rowsQueuedForWrite))
			shutdownDataLog <- true
			break loop
		}
	}
	log.Printf("datalog.go: dataLogWriter() shutting down\n")
}

func dataLog() {
	dataLogStarted = true
	log.Printf("datalog.go: dataLog() started\n")
	shutdownDataLog = make(chan bool)

	db, err := sql.Open("sqlite3", dataLogFilef)
	if err != nil {
		log.Printf("sql.Open(): %s\n", err.Error())
	}

	defer func() {
		db.Close()
		dataLogStarted = false
		log.Printf("datalog.go: dataLog() has closed DB in %s\n", dataLogFilef)
	}()

	_, err = db.Exec("PRAGMA journal_mode=WAL")
	if err != nil {
		log.Printf("db.Exec('PRAGMA journal_mode=WAL') err: %s\n", err.Error())
	}
	// can potentially corrupt DB...
	//_, err = db.Exec("PRAGMA synchronous=OFF")
	//if err != nil {
	//	log.Printf("db.Exec('PRAGMA journal_mode=WAL') err: %s\n", err.Error())
	//}

	//log.Printf("Starting dataLogWriter\n") // REMOVE -- DEBUG
	

	// Do we need to create/update the database schema?
	makeTable(StratuxTimestamp{}, "timestamp", db)
	makeTable(mySituation, "mySituation", db)
	makeTable(globalStatus, "status", db)
	makeTable(globalSettings, "settings", db)
	makeTable(TrafficInfo{}, "traffic", db)
	makeTable(msg{}, "messages", db)
	makeTable(esmsg{}, "es_messages", db)
	makeTable(Dump1090TermMessage{}, "dump1090_terminal", db)
	makeTable(gpsPerfStats{}, "gps_attitude", db)
	makeTable(StratuxStartup{}, "startup", db)

	// Create indices
	db.Exec("CREATE INDEX IF NOT EXISTS timestamp_index ON timestamp(StartupID, SystemClock_value)")
	db.Exec("CREATE INDEX IF NOT EXISTS situation_index ON mySituation(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS status_index ON status(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS settings_index ON settings(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS traffic_index ON traffic(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS messages_index ON messages(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS es_messages_index ON es_messages(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS dump1090_terminal_index ON dump1090_terminal(timestamp_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS gps_attitude_index ON gps_attitude(timestamp_id)")

	// The first entry to be created is the "startup" entry.
	stratuxStartupID = insertData(StratuxStartup{}, "startup", db, 0)
	go dataLogWriter(db)

	dataLogReadyToWrite = true
	//log.Printf("Entering dataLog read loop\n") //REMOVE -- DEBUG

	<-shutdownDataLog // Received a message on the channel to complete a graceful shutdown (see the 'defer func()...' statement above).
	log.Printf("datalog.go: dataLog() received shutdown message\n")

	log.Printf("datalog.go: dataLog() shutting down\n")
	close(shutdownDataLog)
}

/*
	logSituation(), logStatus(), ... pass messages from other functions to the logging
		engine. These are only read into `dataLogChan` if the Replay Log is toggled on,
		and if the log system is ready to accept writes.
*/

func isDataLogReady() bool {
	return dataLogReadyToWrite
}

func logSituation() {
	if globalSettings.ReplayLogSituation && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{key: "situation", tbl: "mySituation", data: mySituation}
	}
}

func logStatus() {
	if globalSettings.ReplayLogStatus && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{key: "status", tbl: "status", data: globalStatus}
	}
}

func logSettings() {
	if globalSettings.ReplayLogStatus && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{key: "settings", tbl: "settings", data: globalSettings}
	}
}

func logTraffic(ti TrafficInfo) {
	if globalSettings.ReplayLogTraffic && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{key: fmt.Sprint(ti.Icao_addr), tbl: "traffic", data: ti}
	}
}

func logMsg(m msg) {
	if globalSettings.ReplayLogDebugMessages && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{tbl: "messages", data: m}
	}
}

func logESMsg(m esmsg) {
	if globalSettings.ReplayLogDebugMessages && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{tbl: "es_messages", data: m}
	}
}

func logGPSAttitude(gpsPerf gpsPerfStats) {
	if globalSettings.ReplayLogDebugMessages && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{tbl: "gps_attitude", data: gpsPerf}
	}
}

func logDump1090TermMessage(m Dump1090TermMessage) {
	if globalSettings.ReplayLogDebugMessages && isDataLogReady() {
		dataLogWriteChan <- DataLogRow{tbl: "dump1090_terminal", data: m}
	}
}

func initDataLog() {
	//log.Printf("dataLogStarted = %t. dataLogReadyToWrite = %t\n", dataLogStarted, dataLogReadyToWrite) //REMOVE -- DEBUG
	insertString = make(map[string]string)
	insertBatchIfs = make(map[string][][]interface{})
	go dataLogWatchdog()

	//log.Printf("datalog.go: initDataLog() complete.\n") //REMOVE -- DEBUG
}

/*
	dataLogWatchdog(): Watchdog function to control startup / shutdown of data logging subsystem.
		Called by initDataLog as a goroutine. It iterates once per second to determine if
		globalSettings.ReplayLog has toggled. If logging was switched from off to on, it starts
		datalog() as a goroutine. If the log is running and we want it to stop, it calls
		closeDataLog() to turn off the input channels, close the log, and tear down the dataLog
		and dataLogWriter goroutines.
*/

func anyReplayLogEnabled() bool {
	if globalStatus.ReplayActive {
		return false
	}
	return globalSettings.ReplayLogDebugMessages || globalSettings.ReplayLogSituation || globalSettings.ReplayLogStatus || globalSettings.ReplayLogTraffic;
}

func dataLogWatchdog() {
	for {
		if !dataLogStarted && anyReplayLogEnabled() { // case 1: sqlite logging isn't running, and we want to start it
			log.Printf("datalog.go: Watchdog wants to START logging.\n")
			go dataLog()
		} else if dataLogStarted && !anyReplayLogEnabled() { // case 2:  sqlite logging is running, and we want to shut it down
			log.Printf("datalog.go: Watchdog wants to STOP logging.\n")
			closeDataLog()
		}
		//log.Printf("Watchdog iterated.\n") //REMOVE -- DEBUG
		time.Sleep(1 * time.Second)
		//log.Printf("Watchdog sleep over.\n") //REMOVE -- DEBUG
	}
}

/*
	closeDataLog(): Handler for graceful shutdown of data logging goroutines. It is called by
		by dataLogWatchdog(), gracefulShutdown(), and by any other function (disk space monitor?)
		that needs to be able to shut down sqlite logging without corrupting data or blocking
		execution.

		This function turns off log message reads into the dataLogChan receiver, and sends a
		message to a quit channel ('shutdownDataLogWriter`) in dataLogWriter(). dataLogWriter()
		then sends a message to a quit channel to 'shutdownDataLog` in dataLog() to close *that*
		goroutine. That function sets dataLogStarted=false once the logfile is closed. By waiting
		for that signal, closeDataLog() won't exit until the log is safely written. This prevents
		data loss on shutdown.
*/

func closeDataLog() {
	//log.Printf("closeDataLog(): dataLogStarted = %t\n", dataLogStarted) //REMOVE -- DEBUG
	dataLogReadyToWrite = false // prevent any new messages from being sent down the channels
	log.Printf("datalog.go: Starting data log shutdown\n")
	shutdownDataLogWriter <- true      //
	defer close(shutdownDataLogWriter) // ... and close the channel so subsequent accidental writes don't stall execution
	log.Printf("datalog.go: Waiting for shutdown signal from dataLog()")
	for dataLogStarted {
		//log.Printf("closeDataLog(): dataLogStarted = %t\n", dataLogStarted) //REMOVE -- DEBUG
		time.Sleep(50 * time.Millisecond)
	}
	log.Printf("datalog.go: Data log shutdown successful.\n")
}
