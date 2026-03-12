package worker

import (
	"fmt"
	"runtime/debug"
)

type recoveredPanicError struct {
	task      string
	recovered any
}

func (e *recoveredPanicError) Error() string {
	return fmt.Sprintf("%s panic: %v", e.task, e.recovered)
}

func (bw *BaseWorker) executeRecoverable(task string, fn func() error) (err error) {
	defer bw.recoverPanicAsError(task, &err)
	return fn()
}

func (bw *BaseWorker) executeWithRecovery(task string, fn func()) {
	go func() {
		defer bw.recoverPanic(task)
		fn()
	}()
}

func (bw *BaseWorker) recoverPanic(task string) {
	if recovered := recover(); recovered != nil {
		bw.logger.Error("Recovered panic",
			"task", task,
			"panic", recovered,
			"stack", string(debug.Stack()),
		)
	}
}

func (bw *BaseWorker) recoverPanicAsError(task string, errp *error) {
	if recovered := recover(); recovered != nil {
		bw.logger.Error("Recovered panic",
			"task", task,
			"panic", recovered,
			"stack", string(debug.Stack()),
		)
		*errp = &recoveredPanicError{
			task:      task,
			recovered: recovered,
		}
	}
}
