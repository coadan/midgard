package bragi

const Version = "1.0"

type Operation string

const (
	OpCreate         Operation = "create"
	OpAdd            Operation = "add"
	OpReplace        Operation = "replace"
	OpRemove         Operation = "remove"
	OpLiteralOpen    Operation = "literal.open"
	OpLiteralReplace Operation = "literal.replace"
	OpLiteralAppend  Operation = "literal.append"
	OpLiteralSeal    Operation = "literal.seal"
	OpCommit         Operation = "commit"
)

type ValueKind string

const (
	ValueString ValueKind = "string"
	ValueNumber ValueKind = "number"
	ValueBool   ValueKind = "bool"
	ValueNull   ValueKind = "null"
	ValueRef    ValueKind = "ref"
)

type Value struct {
	Kind   ValueKind `json:"kind"`
	String string    `json:"string,omitempty"`
	Number string    `json:"number,omitempty"`
	Bool   bool      `json:"bool,omitempty"`
}

// Normalization records a narrow, deterministic source recovery performed by
// the decoder. It is retained in canonical events so recovery is auditable.
type Normalization struct {
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Canonical string `json:"canonical"`
}

type Record struct {
	Operation      Operation       `json:"operation"`
	Target         string          `json:"target"`
	EntityType     string          `json:"entity_type,omitempty"`
	Value          *Value          `json:"value,omitempty"`
	MemberRef      string          `json:"member_ref,omitempty"`
	Line           int             `json:"line"`
	Raw            string          `json:"raw,omitempty"`
	Normalizations []Normalization `json:"normalizations,omitempty"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    int    `json:"line"`
	Raw     string `json:"raw,omitempty"`
}
