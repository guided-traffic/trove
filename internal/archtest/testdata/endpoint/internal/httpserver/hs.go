package httpserver

type Endpoint struct{ name string }

func (e *Endpoint) Name() string { return e.name }

func NewEndpoint(name string) *Endpoint { return &Endpoint{name: name} }

type Chain struct{ Router any }

func (c Chain) Handler() string { return "" }

func inside() { _ = Endpoint{}; _ = Chain{}; _ = NewEndpoint("x") }
