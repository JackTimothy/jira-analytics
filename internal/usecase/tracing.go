package usecase

// noopTracer is the default, so an interactor built without a tracer needs no
// nil checks at any of its call sites.
type noopTracer struct{}

func (noopTracer) Begin(string, map[string]string) Trace { return noopTrace{} }

type noopTrace struct{}

func (noopTrace) Phase(string) func() { return func() {} }
func (noopTrace) End()                {}
