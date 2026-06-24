package promolog

import (
	"context"
	"errors"
	"time"
)

// ErrNoOperation is returned by PromoteOperation when the context carries no
// operation started by StartOperation.
var ErrNoOperation = errors.New("promolog: no operation in context")

// TraceKindOperation marks a Trace produced by a detached operation rather than
// an HTTP request. Request traces leave Trace.Kind empty.
const TraceKindOperation = "operation"

// Operation status values recorded on a promoted operation Trace.
const (
	OperationStatusOK     = "ok"
	OperationStatusFailed = "failed"
)

// OperationAttr is a key/value pair attached to an operation at start time.
// Build one with Attr.
type OperationAttr struct {
	Key   string
	Value string
}

// Attr builds an OperationAttr for StartOperation.
func Attr(key, value string) OperationAttr {
	return OperationAttr{Key: key, Value: value}
}

type operationKeyType struct{}

var operationKey = operationKeyType{}

// Operation is a handle to a detached unit of background work. Its logs are
// buffered for promote-on-error independent of the HTTP request that started
// it. Create one with StartOperation and persist it with PromoteOperation.
type Operation struct {
	id              string
	name            string
	originRequestID string
	parentRequestID string
	attrs           map[string]string
	buf             *Buffer
	startedAt       time.Time
}

// ID returns the operation's unique ID.
func (o *Operation) ID() string { return o.id }

// Name returns the operation name supplied at StartOperation.
func (o *Operation) Name() string { return o.name }

// StartOperation begins a detached operation derived from ctx and returns a
// context carrying the operation plus a handle to it. The returned context
// holds a log buffer keyed to the operation, so records logged through a
// promolog Handler are captured even after the caller detaches request
// cancellation with context.WithoutCancel.
//
// When ctx carries a request ID (set by CorrelationMiddleware), it is preserved
// as the operation's originating request ID for correlation. StartOperation
// does not detach cancellation; apply context.WithoutCancel to the returned
// context before handing it to work that must outlive the request.
func StartOperation(ctx context.Context, name string, attrs ...OperationAttr) (context.Context, *Operation) {
	op := &Operation{
		id:              generateID(),
		name:            name,
		originRequestID: GetRequestID(ctx),
		parentRequestID: GetParentRequestID(ctx),
		buf:             &Buffer{},
		startedAt:       time.Now(),
	}
	if len(attrs) > 0 {
		op.attrs = make(map[string]string, len(attrs))
		for _, a := range attrs {
			op.attrs[a.Key] = a.Value
		}
	}
	ctx = context.WithValue(ctx, operationKey, op)
	ctx = context.WithValue(ctx, bufferKey{}, op.buf)
	return ctx, op
}

// GetOperation returns the Operation stored in ctx by StartOperation, or nil.
func GetOperation(ctx context.Context) *Operation {
	op, _ := ctx.Value(operationKey).(*Operation)
	return op
}

// trace builds the Trace persisted by PromoteOperation. A nil err records a
// successful operation rather than skipping promotion.
func (o *Operation) trace(err error, now time.Time) Trace {
	tags := o.buf.Tags()
	if len(o.attrs) > 0 {
		if tags == nil {
			tags = make(map[string]string, len(o.attrs))
		}
		for k, v := range o.attrs {
			if _, ok := tags[k]; !ok {
				tags[k] = v
			}
		}
	}

	status := OperationStatusOK
	var errChain string
	if err != nil {
		status = OperationStatusFailed
		errChain = err.Error()
	}

	return Trace{
		RequestID:       o.id,
		ParentRequestID: o.parentRequestID,
		Kind:            TraceKindOperation,
		OperationID:     o.id,
		OperationName:   o.name,
		OriginRequestID: o.originRequestID,
		Status:          status,
		ErrorChain:      errChain,
		Tags:            tags,
		Entries:         o.buf.Entries(),
		StartedAt:       o.startedAt,
		Duration:        now.Sub(o.startedAt),
		CreatedAt:       now,
	}
}

// PromoteOperation persists the operation carried by ctx to store, recording
// err as the terminal error when non-nil. The store is passed explicitly
// because the core package keeps no global store. It returns ErrNoOperation
// when ctx carries no operation.
func PromoteOperation(ctx context.Context, store Storer, err error) error {
	op := GetOperation(ctx)
	if op == nil {
		return ErrNoOperation
	}
	return store.Promote(ctx, op.trace(err, time.Now()))
}
