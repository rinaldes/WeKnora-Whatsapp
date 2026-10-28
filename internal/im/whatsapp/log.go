package whatsapp

import (
	"context"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/logger"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// debugEnabled reports whether whatsmeow debug/info logs are forwarded.
// Off by default: whatsmeow is chatty and logs protocol details.
func debugEnabled() bool {
	return os.Getenv("WHATSAPP_DEBUG") == "1"
}

// waLogger bridges whatsmeow's logger onto WeKnora's logger. Warnings and
// errors always pass through; info and debug only when WHATSAPP_DEBUG=1.
type waLogger struct {
	module string
	debug  bool
}

func newWALogger(module string) waLog.Logger {
	return &waLogger{module: module, debug: debugEnabled()}
}

func (l *waLogger) prefix(msg string) string {
	return fmt.Sprintf("[WhatsApp/%s] %s", l.module, msg)
}

func (l *waLogger) Warnf(msg string, args ...any) {
	logger.Warnf(context.Background(), l.prefix(msg), args...)
}

func (l *waLogger) Errorf(msg string, args ...any) {
	logger.Errorf(context.Background(), l.prefix(msg), args...)
}

func (l *waLogger) Infof(msg string, args ...any) {
	if l.debug {
		logger.Infof(context.Background(), l.prefix(msg), args...)
	}
}

func (l *waLogger) Debugf(msg string, args ...any) {
	if l.debug {
		logger.Debugf(context.Background(), l.prefix(msg), args...)
	}
}

func (l *waLogger) Sub(module string) waLog.Logger {
	return &waLogger{module: l.module + "/" + module, debug: l.debug}
}
