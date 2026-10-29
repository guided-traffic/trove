package proxy

// Session-level statements on the request path.
const (
	SetSession = "SET statement_timeout = '5s'"
	SetTo      = "SET search_path TO app"
	Reset      = "RESET ALL"
	SetConfig  = "SELECT set_config('app.x', '1', false)"
	Temp       = "CREATE TEMP TABLE scratch (id int)"
	Hold       = "DECLARE c CURSOR WITH HOLD FOR SELECT 1"
	Serial     = "BEGIN ISOLATION LEVEL SERIALIZABLE"
)

// Fine statements.
const (
	SetLocal  = "SET LOCAL statement_timeout = '5s'"
	SetConfOK = "SELECT set_config('app.x', '1', true)"
	Update    = "UPDATE repo SET name = $1 WHERE id = $2"
	ReadComm  = "BEGIN ISOLATION LEVEL READ COMMITTED"
	English   = "set up the cache to listen on the port"
	Prose     = "set valkey.tls.ca_file to the CA of the servers"
)

// A comment may name LISTEN foo, NOTIFY bar and CREATE TEMP TABLE without being a statement.
