package main

import (
	"database/sql"
	"io"

	_ "github.com/mattn/go-sqlite3"
)

// Fetches all data from a boot ordered by timestamp, calling the respective callbacks as available
func getAllData(db *sql.DB, bootid int, callback func(SituationData), statusCb func(status), trafficCb func(TrafficInfo)) error {
	// Because the various tables have overlapping field names, we can't just join them all together.
	// However, we do want to process in proper order.. Therefore we need multiple queries and loop over the timestamps
	// here manually.
	rows, err := db.Query("select id from timestamp where StartupId=? ORDER BY StratuxClock_value", bootid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tsid uint64
		err = rows.Scan(&tsid)
		if err != nil {
			return err
		}
		defer rows.Close()

		// Fetch situation data
		situationRows, err := db.Query("select * from mySituation WHERE timestamp_id=?", tsid)
		if err != nil {
			return err
		}
		defer situationRows.Close()
		for situationRows.Next() {
			var sit SituationData
			err = rows.Scan(&sit)
			if err != nil {
				return err
			}
			//situationCb(sit)
		}
	}
	return nil
}


func openDb() (*sql.DB, error) {
	return sql.Open("sqlite3", dataLogFilef)
}

func exportGpx(bootids []int, dst io.Writer) error {
	db, err := openDb()
	if err != nil {
		return err
	}

	for _, bootid := range bootids {
		getAllData(db, bootid, func(situation SituationData) {

		}, nil, nil)
	}
	return nil
}

func exportKml(bootids []int, dst io.Writer) error {
	return nil

}