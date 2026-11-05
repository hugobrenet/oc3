package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/opensvc/oc3/feeder"
)

type (
	// arrayRows are the rows an array report makes, in the units of the
	// tables: sizes in MB.
	arrayRows struct {
		// Name is the name of the array in stor_array, and the
		// disk_arrayid of its disks.
		Name     string
		Model    string
		Cache    int64
		Firmware string

		DGs    []arrayDG
		TgtIDs []string
		Disks  []arrayDisk

		// SvcdiskPrefix, when set, has the job rewrite the id of the
		// disks the nodes see, as an hds array: the svcdisks whose id
		// starts with it are indexed by their ldev, and a disk of the
		// same Ldev takes the id the nodes know it by.
		SvcdiskPrefix string
	}

	arrayDG struct {
		Name string
		Free int64
		Used int64
		Size int64
	}

	arrayDisk struct {
		ID    string
		Name  string
		DevID string
		Size  int64
		Alloc int64
		Raid  string
		Group string

		// Ldev is the ldev of a disk of an hds array, lowercase hex: see
		// SvcdiskPrefix.
		Ldev string
	}

	// arrayParser makes the rows of the array name from the sections of its
	// report.
	arrayParser func(name string, data map[string]json.RawMessage) (*arrayRows, error)
)

// arrayParsers are the parsers by array type. A type absent has its reports
// refused by the feeder: see feeder.ArrayRequiredSections.
var arrayParsers = map[feeder.ArrayType]arrayParser{
	feeder.Hds:       parseHds,
	feeder.Hp3par:    parseHp3par,
	feeder.Pure:      parsePure,
	feeder.Symmetrix: parseSymmetrix,
	feeder.Xtremio:   parseXtremio,
}

// arraySection decodes the section of a report into v.
func arraySection(data map[string]json.RawMessage, section string, v any) error {
	b, ok := data[section]
	if !ok {
		return fmt.Errorf("missing section %s", section)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("section %s: %w", section, err)
	}
	return nil
}

// mb returns a size in bytes in MB, rounded down, as the old collector
// computed it.
func mb(b int64) int64 {
	return b / 1024 / 1024
}

// flexInt is an integer an array api sends as a json number or as a string,
// the xtremio api for one.
type flexInt int64

func (t *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*t = 0
		return nil
	}
	i, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("not an integer: %s", b)
	}
	*t = flexInt(i)
	return nil
}
