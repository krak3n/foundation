// Package exit is the seam through which foundation ends the process, so that
// tests can observe an exit instead of suffering one.
package exit

import "os"

// Func ends the process with the given status code. It is [os.Exit] unless a
// test has replaced it, which is what [go.krak3n.dev/foundation/foundationtest.ExitRecorder]
// does.
var Func = os.Exit
