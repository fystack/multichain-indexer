package worker

// WorkerMode defines the operating mode for a worker.
type WorkerMode string

const (
	ModeRegular   WorkerMode = "regular"
	ModeCatchup   WorkerMode = "catchup"
	ModeRescanner WorkerMode = "rescanner"
	ModeManual    WorkerMode = "manual"
)

type FailedBlockEvent struct {
	Chain   string
	Block   uint64
	Attempt int
}

// Worker is the interface implemented by all worker types.
type Worker interface {
	Start()
	Stop()
}
