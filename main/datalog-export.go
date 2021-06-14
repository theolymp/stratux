package main

import (
	"database/sql"
	"fmt"
	"io"
	"reflect"

	_ "github.com/mattn/go-sqlite3"
)

func fillStructFromRow(row *sql.Rows, dest interface{}) error {
	colnames, _ := row.Columns()
	colvals := make([]interface{}, len(colnames))
	colptrs := make([]interface{}, len(colnames))
	for k, _ := range colvals {
		colptrs[k] = &colvals[k]
	}
	err := row.Scan(colptrs...)
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	refl := reflect.ValueOf(dest)

	for i, column := range colnames {
		value := colvals[i]

		f := refl.FieldByName(column)
		if f.IsValid() && f.CanSet() {
			fmt.Println(value)
			fmt.Println(f.Kind())
		}
	}
	return nil

}


// Fetches all MySituations for the respective boot
func getMyTrack(db *sql.DB, bootid int, resultCb func(ts StratuxTimestamp, sit SituationData)) error {
	rows, err := db.Query("select * from timestamp inner join mySituation on timestamp.id=mySituation.timestamp_id " +
						"where StartupId=? AND SystemClock_value is not null order by StratuxClock_value", bootid)

	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts StratuxTimestamp
		var sit SituationData
		fillStructFromRow(rows, ts)
		fillStructFromRow(rows, sit)
		resultCb(ts, sit)
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
		getMyTrack(db, bootid, func(ts StratuxTimestamp, situation SituationData) {

		})
	}
	return nil
}

func exportKml(bootids []int, dst io.Writer) error {
	return nil

}