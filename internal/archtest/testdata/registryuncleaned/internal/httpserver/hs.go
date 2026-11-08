package httpserver

type Router struct{}

func (*Router) HandleRegistryUncleaned(string, any) {}
func (*Router) HandleUncleaned(string, any)         {}
