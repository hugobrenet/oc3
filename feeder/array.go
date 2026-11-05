package feeder

// ArrayRequiredSections lists, by type, the sections an array report must
// hold: the ones its parser builds the rows of the array from. The other
// sections a driver reports are optional, as some arrays do not have them.
//
// A type the collector does not parse yet is absent: its reports are
// refused, rather than stored for nothing.
var ArrayRequiredSections = map[ArrayType][]string{
	Hds:       {"array", "lu", "port", "pool"},
	Hp3par:    {"showvv", "showcpg", "showsys", "shownode", "showport", "showversion"},
	Pure:      {"arrays", "volumes", "ports"},
	Symmetrix: {"sym_info", "sym_dir_info", "sym_dev_info", "sym_dev_wwn_info"},
	Xtremio:   {"clusters_details", "volumes_details", "targets_details"},
}
