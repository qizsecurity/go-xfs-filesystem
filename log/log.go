// Package log holds the logger of the xfs package.
//
// The logger is off by default: the library writes nothing to stdout or stderr
// on its own. A program that wants the library's log lines calls SetLogger.
// No log line holds bytes read from the disk.
package log

import (
	"go.uber.org/zap"
)

var Logger = zap.NewNop().Sugar()

// SetLogger sets the logger of the library. A nil logger switches it off.
func SetLogger(l *zap.SugaredLogger) {
	if l == nil {
		l = zap.NewNop().Sugar()
	}
	Logger = l
}
