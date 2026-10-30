package webish

import "example.test/fix/internal/authz"

func viaZeroVar() { var s authz.Subject; _ = s }
