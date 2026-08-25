package d1

import . "github.com/tinywasm/fmt"

const errPrefix = "d1: "

var ErrDatabaseNotFound = Err(errPrefix + "database not found")
