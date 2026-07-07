package artifact

const (
	TypeReport  = "report"
	TypePayload = "payload"

	StateDraft    = "draft"
	StateSealed   = "sealed"
	StateRejected = "rejected"
)

type Record struct {
	Path         string
	Type         string
	State        string
	Checksum     string
	Size         int64
	ProducerRole string
	PayloadType  string
	Lang         string
}

func (r Record) Ref() string {
	return "artifact:" + r.Path
}

func (r Record) CanExecute() bool {
	return r.Type == TypePayload && r.State == StateSealed
}
