package logger

import (
	"github.com/lni/dragonboat/v3/logger"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/log"
)

// Import this class for the side effect of quieting the
// raft logger.

type nullLogger struct{}

func (nullLogger) SetLevel(logger.LogLevel)                    {}
func (nullLogger) Debugf(format string, args ...interface{})   {}
func (nullLogger) Infof(format string, args ...interface{})    {}
func (nullLogger) Warningf(format string, args ...interface{}) {}
func (nullLogger) Errorf(format string, args ...interface{})   {}
func (nullLogger) Panicf(format string, args ...interface{})   {}

type dbCompatibleLogger struct {
	log.Logger
}

// Don't panic in server code.
func (l *dbCompatibleLogger) Panicf(format string, args ...interface{}) {
	l.Errorf(format, args...)
}

// Ignore SetLevel commands.
func (l *dbCompatibleLogger) SetLevel(level logger.LogLevel) {}

func init() {
	logger.SetLoggerFactory(func(pkgName string) logger.ILogger {
		switch pkgName {
		case "raft", "rsm", "transport", "dragonboat", "raftpb", "logdb":
			// Make the raft library be quieter.
			return &nullLogger{}
		default:
			l := log.NamedSubLogger(pkgName)
			return &dbCompatibleLogger{l}
		}
	})
}
