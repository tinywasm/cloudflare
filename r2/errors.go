package r2

import . "github.com/tinywasm/fmt"

const errPrefix = "r2: "

var ErrBucketNotFound = Err(errPrefix + "bucket not found")
