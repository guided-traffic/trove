package name

// ValidateNew applies npm's rules for the name of a newly published package
// (R-NPM-002, validate-npm-package-name): 1 to MaxNewLength characters
// including the scope; scope and name each made of lower-case ASCII letters,
// digits, ".", "_" and "-", which is what encodeURIComponent leaves unchanged
// once "~'!()*" and capitals are excluded; neither starting with ".", "_" or
// "-" (Q-NPM-14); an unscoped name is not a Node.js core module name,
// "node_modules" or "favicon.ico". Every violation is an npm_invalid_name
// error.
func ValidateNew(n Name) error {
	full := n.String()
	if n.pkg == "" {
		return invalid(full, "the name is empty")
	}
	if len(full) > MaxNewLength {
		return invalid(full, "the name is longer than 214 characters")
	}
	if n.scope != "" {
		if err := validatePart(full, n.scope); err != nil {
			return err
		}
	}
	if err := validatePart(full, n.pkg); err != nil {
		return err
	}
	if n.scope == "" {
		if n.pkg == "node_modules" || n.pkg == "favicon.ico" {
			return invalid(full, "the name is not allowed")
		}
		if IsCoreModule(n.pkg) {
			return invalid(full, "the name is a Node.js core module name")
		}
	}
	return nil
}

// ParseNew parses an already decoded name for publishing: Parse, then
// ValidateNew.
func ParseNew(decoded string) (Name, error) {
	n, err := Parse(decoded)
	if err != nil {
		return Name{}, err
	}
	if err := ValidateNew(n); err != nil {
		return Name{}, err
	}
	return n, nil
}

func validatePart(full, part string) error {
	if part == "" {
		return invalid(full, "the scope or the name is empty")
	}
	switch part[0] {
	case '.', '_', '-':
		return invalid(full, "the scope and the name cannot start with \".\", \"_\" or \"-\"")
	}
	for i := 0; i < len(part); i++ {
		c := part[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		case c >= 'A' && c <= 'Z':
			return invalid(full, "the name cannot contain capital letters")
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			return invalid(full, "the name cannot contain whitespace")
		case c == '~' || c == '\'' || c == '!' || c == '(' || c == ')' || c == '*':
			return invalid(full, "the name cannot contain the characters ~'!()*")
		default:
			return invalid(full, "the name can only contain URL-friendly characters")
		}
	}
	return nil
}

// IsCoreModule reports whether s is the name of a Node.js core module that
// can be required without the "node:" prefix. The list is
// validate-npm-package-name 8.0.0's builtin list without the entries that
// start with "_" or hold a "/", which ValidateNew refuses anyway. A switch in
// a function, not a package variable, keeps the list free of shared state
// (R-ARCH-032).
func IsCoreModule(s string) bool {
	switch s {
	case "assert", "async_hooks", "buffer", "child_process", "cluster", "console",
		"constants", "crypto", "dgram", "diagnostics_channel", "dns", "domain",
		"events", "fs", "http", "http2", "https", "inspector", "module", "net",
		"os", "path", "perf_hooks", "process", "punycode", "querystring",
		"readline", "repl", "stream", "string_decoder", "sys", "timers", "tls",
		"trace_events", "tty", "url", "util", "v8", "vm", "wasi",
		"worker_threads", "zlib":
		return true
	}
	return false
}
